"""WDL tasks of runs without a shared workspace exchange files through S3."""

import logging
import os

import pytest

from WDL.runtime import config

import miniwdl_conquest_tes as plugin
from miniwdl_conquest_tes import s3stage

LOG = logging.getLogger("test")


class FakeS3:
    """Enough of boto3's S3 client, backed by a dict."""

    def __init__(self):
        self.objects = {}
        self.uploads = []

    def upload_file(self, path, bucket, key):
        with open(path, "rb") as f:
            self.objects[(bucket, key)] = f.read()
        self.uploads.append(f"{bucket}/{key}")

    def download_file(self, bucket, key, dest):
        with open(dest, "wb") as f:
            f.write(self.objects[(bucket, key)])

    def get_paginator(self, _name):
        fake = self

        class Paginator:
            def paginate(self, Bucket, Prefix):
                keys = sorted(k for b, k in fake.objects if b == Bucket and k.startswith(Prefix))
                yield {"Contents": [{"Key": k} for k in keys]}

        return Paginator()


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def test_upload_input_uploads_once_and_reuses():
    s3, index = FakeS3(), s3stage.Index()
    index.put("/runner/call-a/work/out.txt", "s3://b/run/call-a/work/out.txt")

    url = s3stage.upload_input(s3, index, "/runner/call-a/work/out.txt", "s3://b/run/call-b/inputs/0/out.txt")

    assert url == "s3://b/run/call-a/work/out.txt"
    assert s3.uploads == []


def test_upload_input_file_and_directory(tmp_path):
    s3, index = FakeS3(), s3stage.Index()
    write(str(tmp_path / "in.txt"), "x")
    write(str(tmp_path / "ref" / "a" / "b.fa"), "y")

    file_url = s3stage.upload_input(s3, index, str(tmp_path / "in.txt"), "s3://b/p/inputs/0/in.txt")
    dir_url = s3stage.upload_input(s3, index, str(tmp_path / "ref") + "/", "s3://b/p/inputs/1/ref/")

    assert file_url == "s3://b/p/inputs/0/in.txt"
    assert dir_url == "s3://b/p/inputs/1/ref/"
    assert sorted(s3.uploads) == ["b/p/inputs/0/in.txt", "b/p/inputs/1/ref/a/b.fa"]
    # a second task taking the same input doesn't upload it again
    s3stage.upload_input(s3, index, str(tmp_path / "in.txt"), "s3://b/q/inputs/0/in.txt")
    assert len(s3.uploads) == 2


def test_download_work_dir_skips_inputs_and_indexes_files(tmp_path):
    s3, index = FakeS3(), s3stage.Index()
    s3.objects[("b", "p/work/out.txt")] = b"result"
    s3.objects[("b", "p/work/sub/x.bam")] = b"bam"
    s3.objects[("b", "p/work/_miniwdl_inputs/0/in.txt")] = b"input"
    s3.objects[("b", "p/workother/y")] = b"not ours"

    count = s3stage.download_work_dir(s3, index, "s3://b/p/work", str(tmp_path))

    assert count == 2
    assert (tmp_path / "out.txt").read_bytes() == b"result"
    assert (tmp_path / "sub" / "x.bam").read_bytes() == b"bam"
    assert not (tmp_path / "_miniwdl_inputs").exists()
    assert index.get(str(tmp_path / "sub" / "x.bam")) == "s3://b/p/work/sub/x.bam"


def test_download_work_dir_refuses_paths_outside(tmp_path):
    s3 = FakeS3()
    s3.objects[("b", "p/work/../../escape")] = b"x"

    with pytest.raises(ValueError):
        s3stage.download_work_dir(s3, s3stage.Index(), "s3://b/p/work", str(tmp_path / "w"))


@pytest.fixture
def container(tmp_path, monkeypatch):
    monkeypatch.setattr(plugin.ConquestTESContainer, "_s3_work_uri", "s3://conquest/v1/workflow-runs/r1/work/wdl")
    monkeypatch.setattr(plugin.ConquestTESContainer, "_s3_client", FakeS3())
    monkeypatch.setattr(plugin.ConquestTESContainer, "_s3_index", s3stage.Index())
    host_dir = tmp_path / "runs" / "wf" / "call-hello"
    host_dir.mkdir(parents=True)
    c = plugin.ConquestTESContainer(config.Loader(LOG), "call-hello", str(host_dir))
    c.runtime_values = {"docker": "ubuntu:22.04"}
    return c


def test_s3_task_declares_inputs_command_and_work_dir(container, tmp_path):
    write(str(tmp_path / "data" / "names.txt"), "a\nb\n")
    write(str(tmp_path / "data" / "refdir" / "r.fa"), ">r\n")
    container.add_paths([str(tmp_path / "data" / "names.txt"), str(tmp_path / "data" / "refdir") + "/"])

    task = container._build_s3_task(LOG, "cat names.txt")

    cdir = "/mnt/miniwdl_task_container"
    assert container.container_dir == cdir
    base = "s3://conquest/v1/workflow-runs/r1/work/wdl/" + container.host_dir.strip("/") + "/attempt-1"
    by_path = {i.path: i for i in task.inputs}
    assert by_path[f"{cdir}/in/0/names.txt"].url.startswith(base + "/inputs/")
    assert by_path[f"{cdir}/in/0/refdir"].type == "DIRECTORY"
    assert by_path[f"{cdir}/in/command"].content == "cat names.txt"
    assert {(o.path, o.url, o.type) for o in task.outputs} == {
        (f"{cdir}/work", base + "/work", "DIRECTORY"),
        (f"{cdir}/streams/stdout.txt", base + "/stdout.txt", "FILE"),
        (f"{cdir}/streams/stderr.txt", base + "/stderr.txt", "FILE"),
    }
    # Funnel mounts all inputs at their common ancestor, and each output
    # (its directory for FILE outputs); none of these may contain another
    input_root = os.path.commonpath([i.path for i in task.inputs])
    mounts = [input_root] + [o.path if o.type == "DIRECTORY" else os.path.dirname(o.path) for o in task.outputs]
    for a in mounts:
        for b in mounts:
            assert a == b or not (b + "/").startswith(a + "/"), (a, b)
    assert task.tags is None or "conquest_workspace_scope" not in task.tags
    assert task.executors[0].workdir == f"{cdir}/work"


def test_collected_outputs_resolve_like_a_local_container(container):
    base = container._s3_attempt_uri()
    bucket, key = s3stage.split_s3(base)
    s3 = container._s3_client
    s3.objects[(bucket, key + "/work/out/greeting.txt")] = b"hello\n"
    s3.objects[(bucket, key + "/stdout.txt")] = b"done\n"

    container._collect_s3_outputs(LOG, best_effort=False)

    # miniwdl maps the container path of an output to the downloaded file
    host = container.host_path("/mnt/miniwdl_task_container/work/out/greeting.txt")
    assert host is not None and open(host).read() == "hello\n"
    assert open(container.host_stdout_txt()).read() == "done\n"
    # a later task taking it as input names the same object
    assert container._s3_index.get(host) == f"s3://{bucket}/{key}/work/out/greeting.txt"


def test_retries_use_a_fresh_prefix(container):
    first = container._s3_attempt_uri()
    container.try_counter += 1

    assert container._s3_attempt_uri() != first
    assert container._s3_attempt_uri().endswith("/attempt-2")


def test_shared_workspace_mode_is_unchanged(tmp_path, monkeypatch):
    monkeypatch.setattr(plugin.ConquestTESContainer, "_s3_work_uri", "")
    host_dir = tmp_path / "call"
    host_dir.mkdir()

    c = plugin.ConquestTESContainer(config.Loader(LOG), "call", str(host_dir))
    c.runtime_values = {"docker": "ubuntu:22.04"}
    task = c._build_tes_task("true")

    assert c.container_dir == str(host_dir)
    assert task.tags["conquest_workspace_scope"] == "shared"
    assert not task.inputs and not task.outputs


def test_inputs_resolve_back_to_their_host_paths(container, tmp_path):
    write(str(tmp_path / "in" / "a.txt"), "a")
    container.add_paths([str(tmp_path / "in" / "a.txt")])

    container_path = container.input_path_map[str(tmp_path / "in" / "a.txt")]

    assert container_path == "/mnt/miniwdl_task_container/in/0/a.txt"
    assert container.host_path(container_path) == str(tmp_path / "in" / "a.txt")


def run_dereference(directory):
    """Run the plugin's link dereferencing on a directory with bash."""
    import subprocess

    subprocess.run(
        ["bash", "-c", f"{plugin.DEREFERENCE_LINKS} {plugin._shell_quote(str(directory))}"],
        check=True,
    )


def test_links_in_the_work_dir_become_copies(tmp_path):
    inputs, work = tmp_path / "in", tmp_path / "work"
    write(str(inputs / "ref.fa"), ">ref\n")
    write(str(inputs / "refdir" / "a.txt"), "a")
    write(str(work / "real.txt"), "real")
    (work / "sub").mkdir()
    os.symlink(inputs / "ref.fa", work / "ref.fa")
    os.symlink(inputs / "refdir", work / "sub" / "refdir")
    os.symlink("real.txt", work / "same.txt")
    os.symlink(tmp_path / "missing", work / "dangling")

    run_dereference(work)

    links = [p for p in work.rglob("*") if p.is_symlink()]
    assert links == []
    assert (work / "ref.fa").read_text() == ">ref\n"
    assert (work / "sub" / "refdir" / "a.txt").read_text() == "a"
    assert (work / "same.txt").read_text() == "real"
    assert not os.path.lexists(work / "dangling")
    # the targets are untouched
    assert (inputs / "ref.fa").read_text() == ">ref\n"


def test_s3_task_keeps_the_command_exit_code_and_dereferences(container, tmp_path):
    import subprocess

    command = "ln -s /nonexistent dangling; echo out > real.txt; exit 3"
    task = container._build_s3_task(LOG, command)
    script = task.executors[0].command[2]
    # run the executor's script with the container directory moved into
    # tmp_path; the TES server stages the command there as an input
    root = tmp_path / "c"
    (root / "in").mkdir(parents=True)
    (root / "in" / "command").write_text(command)
    local = script.replace(container.container_dir, str(root))

    rc = subprocess.run(["bash", "-c", local]).returncode

    assert rc == 3
    work = root / "work"
    assert (work / "real.txt").read_text() == "out\n"
    assert not os.path.lexists(work / "dangling")
    assert (root / "streams" / "stdout.txt").exists()
