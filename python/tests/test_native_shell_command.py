"""Native-shell tasks must create their outputs' parent directories.

Snakemake creates them on the runner before a job runs, which a task only sees
when it shares the runner's filesystem. In emptydir mode, and on any TES other
than Conquest's own, it doesn't: `echo x > Bonjour/world.txt` failed with
"No such file or directory" (nathanhaigh/snakemake-hello-world@225cc26b).
"""

import subprocess
from pathlib import Path
from types import SimpleNamespace

from snakemake_executor_plugin_conquest_tes import Executor, native_shell_command


def run_bash(command: str, cwd: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["/bin/bash", "-c", command], cwd=cwd, capture_output=True, text=True
    )


def test_creates_output_parent_dirs_before_the_rule_runs(tmp_path):
    command = native_shell_command(
        '\n\t\techo "Bonjour, World!" > Bonjour/world.txt\n\t\t',
        [str(tmp_path / "Bonjour" / "world.txt")],
    )

    result = run_bash(command, tmp_path)

    assert result.returncode == 0, result.stderr
    assert (tmp_path / "Bonjour" / "world.txt").read_text() == "Bonjour, World!\n"


def test_without_the_prefix_the_rule_fails(tmp_path):
    # Guards the test above: the bare command fails in an empty task dir.
    result = run_bash('echo "Bonjour, World!" > Bonjour/world.txt', tmp_path)

    assert result.returncode != 0


def test_does_not_create_the_outputs_themselves(tmp_path):
    # directory() outputs are the rule's to create, as in Snakemake.
    out_dir = tmp_path / "results" / "per_sample"

    result = run_bash(native_shell_command("true", [str(out_dir)]), tmp_path)

    assert result.returncode == 0, result.stderr
    assert (tmp_path / "results").is_dir()
    assert not out_dir.exists()


def test_quotes_paths_and_deduplicates_parents(tmp_path):
    weird = tmp_path / "a dir; touch pwned" / "$(echo x)"
    command = native_shell_command(
        "true", [str(weird / "one.txt"), str(weird / "two.txt"), str(weird / "one.txt")]
    )

    result = run_bash(command, tmp_path)

    assert result.returncode == 0, result.stderr
    assert weird.is_dir()
    assert not (tmp_path / "pwned").exists()
    assert command.splitlines()[0].count(str(weird.name)) == 1


def test_keeps_the_rule_command_and_its_shell_options():
    rule = "set -euo pipefail\nfalse | true"

    command = native_shell_command(rule, ["/work/workflow/out/x.txt"])

    assert command == f"mkdir -p -- /work/workflow/out || exit 1\n{rule}"


def test_no_outputs_leaves_the_command_unchanged():
    assert native_shell_command("echo hi", []) == "echo hi"
    assert native_shell_command("echo hi", ["x.txt", "/top.txt"]) == "echo hi"


class FakeJob:
    jobid = 7
    name = "multilingual_hello_world"
    message = None
    is_shell = True
    container_img_url = None
    shellcmd = 'echo "Bonjour, World!" > Bonjour/world.txt'
    input = []
    log = ["logs/Bonjour.log"]
    benchmark = None
    resources = {}

    def __init__(self):
        self.output = ["Bonjour/world.txt"]

    def is_group(self):
        return False

    def format_wildcards(self, value):
        return value


def emptydir_executor(workflow_root: Path) -> Executor:
    executor = object.__new__(Executor)
    executor.shared_workspace = False
    executor.task_root = Path("/work")
    executor.task_workflow_root = Path("/work/workflow")
    executor.workflow_root = workflow_root
    executor.workflow_workdir = workflow_root
    executor.workflow_workdir_rel = "."
    executor.task_workflow_workdir = Path("/work/workflow")
    executor.workflow_source_files = set()
    executor.run_id = "run-1"
    executor.bundle_uri = "s3://conquest/v1/workflow-runs/run-1/workflow/bundle/"
    executor.workdir_uri = "s3://conquest/v1/workflow-runs/run-1/snakemake/"
    executor.output_dir_uri = "s3://conquest/v1/workflow-runs/run-1/outputs/"
    executor.task_image = "example/image:1"
    executor.jobname = "snakejob.{name}.{jobid}.sh"
    executor.workflow = SimpleNamespace()
    return executor


def test_build_task_creates_dirs_of_outputs_and_logs(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    executor = emptydir_executor(tmp_path)

    task, aux = executor._build_task(FakeJob())

    assert aux["mode"] == "native-shell"
    command = task.executors[0].command
    assert command[:2] == ["/bin/bash", "-c"]
    assert command[2] == (
        "mkdir -p -- /work/workflow/Bonjour /work/workflow/logs || exit 1\n"
        + FakeJob.shellcmd
    )


def post_init_executor(tmp_path, monkeypatch) -> Executor:
    root = tmp_path / "workspace" / "workflow"
    (root / "tests" / "input_files").mkdir(parents=True)
    monkeypatch.chdir(root)
    for name, value in {
        "CONQUEST_WORKFLOW_ROOT": str(root),
        "CONQUEST_RUN_ID": "run-1",
        "CONQUEST_WORKFLOW_BUNDLE_URI": "s3://conquest/run-1/bundle/",
        "CONQUEST_SNAKEMAKE_WORKDIR_URI": "s3://conquest/run-1/snakemake/",
        "CONQUEST_OUTPUT_DIR_URI": "s3://conquest/run-1/outputs/",
        "CONQUEST_TES_URL": "http://conquest/ga4gh/tes",
        "CONQUEST_S3_REGION": "us-east-1",
    }.items():
        monkeypatch.setenv(name, value)
    monkeypatch.delenv("CONQUEST_SHARED_WORKSPACE", raising=False)
    executor = object.__new__(Executor)
    executor.workflow = SimpleNamespace(
        executor_settings=SimpleNamespace(url=None, task_image="example/image:1"),
        remote_execution_settings=SimpleNamespace(container_image=None),
    )
    executor.__post_init__()
    return executor


def test_emptydir_tasks_stage_the_workflow_at_the_runners_path(tmp_path, monkeypatch):
    executor = post_init_executor(tmp_path, monkeypatch)
    root = tmp_path / "workspace" / "workflow"

    assert executor.task_workflow_root == root.resolve()


def test_absolute_paths_in_commands_resolve_in_the_task(tmp_path, monkeypatch):
    # Workflows such as zavolanlab/zarp resolve inputs to absolute paths,
    # which Snakemake writes into the shell command as they are.
    executor = post_init_executor(tmp_path, monkeypatch)
    root = (tmp_path / "workspace" / "workflow").resolve()
    gtf = root / "tests" / "input_files" / "annotation.gtf"
    gtf.write_text("chr1\n")
    executor.jobname = "snakejob.{name}.{jobid}.sh"
    executor.s3_client = None
    job = FakeJob()
    job.input = [str(gtf)]
    job.output = ["results/sorted.gtf"]
    job.log = []
    job.shellcmd = f"sort {gtf} > results/sorted.gtf"
    monkeypatch.setattr(executor, "_upload_local_input", lambda job, path: "s3://conquest/run-1/x")

    task, _ = executor._build_task(job)

    staged = {i.path for i in task.inputs}
    assert str(gtf) in staged
    assert task.executors[0].workdir == str(root)


def test_backend_hint_resource_becomes_a_task_tag(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    job = FakeJob()
    job.resources = {"tes_backend": " funnel-a , local ,"}

    task, _ = emptydir_executor(tmp_path)._build_task(job)

    assert task.tags["conquest.tes_backend"] == "funnel-a,local"


def test_jobs_without_a_backend_hint_get_no_tag(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    for resources in ({}, {"tes_backend": 3}, {"tes_backend": " , "}):
        job = FakeJob()
        job.resources = resources

        task, _ = emptydir_executor(tmp_path)._build_task(job)

        assert "conquest.tes_backend" not in task.tags
