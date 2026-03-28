from __future__ import annotations

import os
from pathlib import Path
from typing import Iterable
from urllib.parse import urlparse

import boto3


def make_client():
    endpoint = os.environ.get("CONQUEST_S3_ENDPOINT")
    region = os.environ.get("CONQUEST_S3_REGION", "us-east-1")
    kwargs = {"region_name": region}
    if endpoint:
        kwargs["endpoint_url"] = endpoint
    return boto3.client("s3", **kwargs)


def parse_s3_url(url: str) -> tuple[str, str]:
    parsed = urlparse(url)
    if parsed.scheme != "s3" or not parsed.netloc or not parsed.path:
        raise ValueError(f"invalid s3 url: {url}")
    return parsed.netloc, parsed.path.lstrip("/")


def join_s3_url(base: str, relative_path: str) -> str:
    bucket, prefix = parse_s3_url(base)
    prefix = prefix.rstrip("/")
    relative_path = relative_path.lstrip("/")
    if relative_path:
        prefix = f"{prefix}/{relative_path}"
    return f"s3://{bucket}/{prefix}"


def upload_file(client, source: Path, dest_url: str):
    bucket, key = parse_s3_url(dest_url)
    client.upload_file(str(source), bucket, key)


def upload_directory(client, source_dir: Path, dest_url: str):
    bucket, prefix = parse_s3_url(dest_url)
    for file_path in sorted(p for p in source_dir.rglob("*") if p.is_file()):
        rel = file_path.relative_to(source_dir).as_posix()
        key = f"{prefix.rstrip('/')}/{rel}"
        client.upload_file(str(file_path), bucket, key)


def download_file(client, source_url: str, dest: Path):
    bucket, key = parse_s3_url(source_url)
    dest.parent.mkdir(parents=True, exist_ok=True)
    client.download_file(bucket, key, str(dest))


def download_directory(client, source_url: str, dest: Path):
    bucket, prefix = parse_s3_url(source_url)
    dest.mkdir(parents=True, exist_ok=True)
    paginator = client.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix.rstrip("/") + "/"):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            rel = key[len(prefix.rstrip("/") + "/") :]
            if not rel:
                continue
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            client.download_file(bucket, key, str(target))


def first_existing_url(urls: Iterable[str]) -> str | None:
    for url in urls:
        if url:
            return url
    return None
