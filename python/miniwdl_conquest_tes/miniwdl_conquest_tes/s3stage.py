"""S3 staging for WDL tasks of runs without a shared workspace.

Each task attempt gets an S3 prefix under ``CONQUEST_WDL_WORK_URI``. Its
inputs are TES inputs (files uploaded from the runner, or reused from where
an earlier task's outputs came from), and its work directory, stdout and
stderr are TES outputs that the plugin downloads back into the runner's task
directory, where miniwdl evaluates the task's outputs as it would after a
local container.
"""

from __future__ import annotations

import os
import threading
from typing import Dict, Iterable, Optional, Tuple
from urllib.parse import urlparse

import boto3

#: Subdirectory of the task work directory where miniwdl places inputs;
#: those are already on the runner, so they aren't downloaded back.
INPUTS_SUBDIR = "_miniwdl_inputs"


def make_client():
    kwargs = {"region_name": os.environ.get("CONQUEST_S3_REGION", "us-east-1")}
    endpoint = os.environ.get("CONQUEST_S3_ENDPOINT", "").strip()
    if endpoint:
        kwargs["endpoint_url"] = endpoint
    return boto3.client("s3", **kwargs)


def split_s3(url: str) -> Tuple[str, str]:
    parsed = urlparse(url)
    if parsed.scheme != "s3" or not parsed.netloc:
        raise ValueError(f"not an s3:// URL: {url}")
    return parsed.netloc, parsed.path.lstrip("/")


def join(url: str, *parts: str) -> str:
    out = url.rstrip("/")
    for part in parts:
        part = part.strip("/")
        if part:
            out += "/" + part
    return out


class Index:
    """Where files on the runner came from, or went to, in S3.

    A file a task produced is downloaded into its call's work directory; a
    later task that takes it as input can then name the same S3 object
    instead of uploading it again. Directories are keyed with a trailing
    slash, as miniwdl does.
    """

    def __init__(self) -> None:
        self._urls: Dict[str, str] = {}
        self._lock = threading.Lock()

    def get(self, host_path: str) -> Optional[str]:
        with self._lock:
            return self._urls.get(host_path)

    def put(self, host_path: str, url: str) -> None:
        with self._lock:
            self._urls[host_path] = url


def upload_input(client, index: Index, host_path: str, dest_url: str) -> str:
    """Return an S3 URL for an input, uploading it unless it is known.

    ``host_path`` ends with "/" for directories.
    """
    known = index.get(host_path)
    if known:
        return known
    bucket, key = split_s3(dest_url)
    if host_path.endswith("/"):
        root = host_path.rstrip("/")
        for dirpath, _, files in os.walk(root):
            for name in sorted(files):
                path = os.path.join(dirpath, name)
                rel = os.path.relpath(path, root).replace(os.sep, "/")
                client.upload_file(path, bucket, f"{key.rstrip('/')}/{rel}")
    else:
        client.upload_file(host_path, bucket, key)
    index.put(host_path, dest_url)
    return dest_url


def download_work_dir(client, index: Index, work_url: str, host_work_dir: str) -> int:
    """Download a task's work directory, except the inputs subdirectory.

    Returns the number of files downloaded. Each file is recorded in the
    index, so later tasks reuse its S3 object.
    """
    bucket, prefix = split_s3(work_url)
    prefix = prefix.rstrip("/") + "/"
    count = 0
    for key in _list(client, bucket, prefix):
        rel = key[len(prefix):]
        if not rel or rel.endswith("/") or rel.split("/", 1)[0] == INPUTS_SUBDIR:
            continue
        dest = os.path.join(host_work_dir, *rel.split("/"))
        if not _within(dest, host_work_dir):
            raise ValueError(f"object {key} would be written outside the work directory")
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        client.download_file(bucket, key, dest)
        index.put(dest, f"s3://{bucket}/{key}")
        count += 1
    return count


def download_optional(client, url: str, dest: str) -> bool:
    """Download one object if it exists; return whether it did."""
    bucket, key = split_s3(url)
    if not any(k == key for k in _list(client, bucket, key)):
        return False
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    client.download_file(bucket, key, dest)
    return True


def _list(client, bucket: str, prefix: str) -> Iterable[str]:
    paginator = client.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            yield obj["Key"]


def _within(path: str, root: str) -> bool:
    path, root = os.path.realpath(path), os.path.realpath(root)
    return path == root or path.startswith(root + os.sep)
