from dataclasses import dataclass, field
import hashlib
import json
import math
import os
from pathlib import Path
from typing import AsyncGenerator, List, Optional

import tes

from snakemake_interface_common.exceptions import WorkflowError
from snakemake_interface_executor_plugins.executors.base import SubmittedJobInfo
from snakemake_interface_executor_plugins.executors.remote import RemoteExecutor
from snakemake_interface_executor_plugins.jobs import JobExecutorInterface
from snakemake_interface_executor_plugins.settings import (
    CommonSettings,
    ExecutorSettingsBase,
)

from . import s3


@dataclass
class ExecutorSettings(ExecutorSettingsBase):
    url: Optional[str] = field(
        default=None,
        metadata={
            "help": "URL of the TES server",
            "required": False,
            "env_var": True,
        },
    )
    task_image: Optional[str] = field(
        default=None,
        metadata={
            "help": "Default image for compatibility-mode tasks",
            "required": False,
            "env_var": True,
        },
    )


common_settings = CommonSettings(
    non_local_exec=True,
    implies_no_shared_fs=True,
    job_deploy_sources=False,
    pass_default_storage_provider_args=False,
    pass_default_resources_args=True,
    pass_envvar_declarations_to_cmd=False,
    auto_deploy_default_storage_provider=False,
    init_seconds_before_status_checks=10,
    can_transfer_local_files=True,
)


@dataclass
class Localization:
    url: str
    path: str
    type: str

    def as_dict(self) -> dict:
        return {"url": self.url, "path": self.path, "type": self.type}


class Executor(RemoteExecutor):
    def __post_init__(self):
        # Detect shared-workspace mode. The backend injects this env var when
        # workspace mode is "pvc" (either RWX or RWO).
        self.shared_workspace = (
            os.environ.get("CONQUEST_SHARED_WORKSPACE") == "true"
        )

        if self.shared_workspace:
            # In shared mode, task absolute paths align with the runner
            # checkout so upstream outputs are visible at the same paths.
            self.task_root = Path("/workspace")
            self.task_workflow_root = self.task_root / "workflow"
        else:
            # Emptydir mode — existing behavior preserved exactly.
            self.task_root = Path("/work")
            self.task_workflow_root = self.task_root / "workflow"

        self.workflow_root = Path(
            os.environ.get("CONQUEST_WORKFLOW_ROOT", os.getcwd())
        ).resolve()
        self.workflow_workdir = Path(
            os.environ.get("CONQUEST_WORKFLOW_WORKDIR", self.workflow_root)
        ).resolve()
        self.workflow_workdir_rel = self.workflow_workdir.relative_to(
            self.workflow_root
        ).as_posix()
        self.task_workflow_workdir = self.task_workflow_root / self.workflow_workdir_rel
        self.workflow_source_files = self._load_workflow_source_files()
        self.run_id = self._required_env("CONQUEST_RUN_ID")
        self.bundle_uri = self._required_env("CONQUEST_WORKFLOW_BUNDLE_URI")
        self.workdir_uri = self._required_env("CONQUEST_SNAKEMAKE_WORKDIR_URI")
        self.output_dir_uri = self._required_env("CONQUEST_OUTPUT_DIR_URI")
        self.task_image = (
            self.workflow.executor_settings.task_image
            or os.environ.get("CONQUEST_SNAKEMAKE_TASK_IMAGE")
            or self.workflow.remote_execution_settings.container_image
        )

        tes_url = self.workflow.executor_settings.url or os.environ.get(
            "CONQUEST_TES_URL"
        )
        if not tes_url:
            raise WorkflowError("TES URL is required")
        self.tes_client = tes.HTTPClient(url=tes_url)
        self.s3_client = s3.make_client()

    def run_job(self, job: JobExecutorInterface):
        if hasattr(job, "prepare"):
            self.workflow.async_run(job.prepare())

        task, aux = self._build_task(job)

        try:
            tes_id = self.tes_client.create_task(task)
        except Exception as err:
            raise WorkflowError(err)

        aux["localizations"] = [entry.as_dict() for entry in aux["localizations"]]
        self.report_job_submission(
            SubmittedJobInfo(job=job, external_jobid=tes_id, aux=aux)
        )

    async def check_active_jobs(
        self, active_jobs: List[SubmittedJobInfo]
    ) -> AsyncGenerator[SubmittedJobInfo, None]:
        unfinished = {"UNKNOWN", "INITIALIZING", "QUEUED", "RUNNING", "PAUSED"}
        failed = {"EXECUTOR_ERROR", "SYSTEM_ERROR", "CANCELED"}

        for job_info in active_jobs:
            try:
                async with self.status_rate_limiter:
                    task = self.tes_client.get_task(
                        job_info.external_jobid, view="MINIMAL"
                    )
            except Exception as err:
                self.logger.warning(
                    f"status lookup failed for task {job_info.external_jobid}: {err}"
                )
                yield job_info
                continue

            if task.state in unfinished:
                yield job_info
                continue
            if task.state in failed:
                self.report_job_error(
                    job_info, msg=f"TES task ended in state {task.state}"
                )
                continue
            if task.state == "COMPLETE":
                try:
                    if not self.shared_workspace:
                        # In emptydir mode, localize outputs from S3 back to
                        # local workspace so downstream tasks can see them.
                        self._localize_outputs(job_info)
                    # In shared-workspace mode, outputs already exist locally
                    # on the shared PVC — skip localization.
                except Exception as err:
                    self.report_job_error(
                        job_info,
                        msg=f"task completed but output localization failed: {err}",
                    )
                    continue
                self.report_job_success(job_info)
                continue
            yield job_info

    def cancel_jobs(self, active_jobs: List[SubmittedJobInfo]):
        for job_info in active_jobs:
            try:
                self.tes_client.cancel_task(job_info.external_jobid)
            except Exception:
                continue

    def _required_env(self, name: str) -> str:
        value = os.environ.get(name, "").strip()
        if not value:
            raise WorkflowError(f"{name} is required")
        return value

    def _load_workflow_source_files(self) -> set[str]:
        index_path = os.environ.get("CONQUEST_WORKFLOW_SOURCE_INDEX", "").strip()
        if not index_path:
            return set()
        try:
            with open(index_path) as handle:
                return set(json.load(handle))
        except Exception as err:
            raise WorkflowError(f"failed to read workflow source index: {err}")

    def _build_task(self, job: JobExecutorInterface):
        native = self._can_run_natively(job)

        # In shared-workspace mode, skip the bundle input — the runner
        # checkout is already on the shared PVC at /workspace/workflow.
        if self.shared_workspace:
            inputs = []
        else:
            inputs = [
                tes.models.Input(
                    url=self.bundle_uri,
                    path=str(self.task_workflow_root),
                    type="DIRECTORY",
                )
            ]

        inputs.extend(self._build_additional_inputs(job))

        outputs, localizations = self._build_outputs(job)
        image = self._task_image_for_job(job, native)
        command = self._command_for_job(job, native)

        tags = {
            "conquest_run_id": self.run_id,
            "conquest_mode": "native-shell" if native else "compat-nested",
            "conquest_job_id": str(job.jobid),
        }

        # In shared-workspace mode, set the tag so the backend mounts
        # the shared PVC subtree instead of an isolated per-task subpath.
        if self.shared_workspace:
            tags["conquest_workspace_scope"] = "shared"

        task = tes.Task(
            name=job.format_wildcards(self.jobname),
            description=self._job_description(job),
            executors=[
                tes.models.Executor(
                    image=image,
                    command=command,
                    workdir=str(self.task_workflow_workdir),
                    env={
                        "HOME": str(self.task_workflow_root),
                        "XDG_CACHE_HOME": str(self.task_workflow_root / ".cache"),
                    },
                )
            ],
            inputs=inputs,
            outputs=outputs,
            resources=self._resources(job),
            tags=tags,
        )

        return task, {
            "mode": "native-shell" if native else "compat-nested",
            "localizations": localizations,
        }

    def _can_run_natively(self, job: JobExecutorInterface) -> bool:
        return bool(
            not job.is_group()
            and getattr(job, "is_shell", False)
            and not getattr(job, "is_shadow", False)
            and not getattr(job, "is_pipe", False)
            and not getattr(job, "is_service", False)
            and getattr(job, "conda_env_spec", None) is None
            and getattr(job, "env_modules", None) is None
        )

    def _task_image_for_job(self, job: JobExecutorInterface, native: bool) -> str:
        if native and getattr(job, "container_img_url", None):
            return job.container_img_url
        return self.task_image

    def _command_for_job(self, job: JobExecutorInterface, native: bool) -> list[str]:
        if native:
            return ["/bin/bash", "-c", job.shellcmd]
        return ["/bin/bash", "-c", self._compat_job_exec(job)]

    def _compat_job_exec(self, job: JobExecutorInterface) -> str:
        command = self.format_job_exec(job)
        if self.workflow_workdir_rel in {"", "."}:
            return command
        for candidate in (
            f" --directory {self.workflow_workdir_rel}",
            f" --directory '{self.workflow_workdir_rel}'",
            f' --directory "{self.workflow_workdir_rel}"',
        ):
            command = command.replace(candidate, "")
        return command

    def _job_description(self, job: JobExecutorInterface) -> str:
        if job.is_group():
            return f"Snakemake group job {job.groupid}"
        message = getattr(job, "message", None)
        if message:
            return message
        return f"Snakemake job {job.name}"

    def _resources(self, job: JobExecutorInterface):
        resources = tes.models.Resources()
        if job.resources.get("_cores") is not None:
            resources.cpu_cores = int(job.resources["_cores"])
        if job.resources.get("mem_mb") is not None:
            resources.ram_gb = math.ceil(job.resources["mem_mb"] / 1000)
        if job.resources.get("disk_mb") is not None:
            resources.disk_gb = math.ceil(job.resources["disk_mb"] / 1000)
        return resources

    def _build_additional_inputs(self, job: JobExecutorInterface) -> list:
        inputs = []
        for iofile in job.input:
            if getattr(iofile, "is_storage", False):
                query = getattr(iofile.storage_object, "query", "")
                if not str(query).startswith("s3://"):
                    raise WorkflowError(
                        f"unsupported storage input {query!r}; only s3:// is supported"
                    )
                inputs.append(
                    tes.models.Input(
                        url=str(query),
                        path=self._task_path(iofile),
                        type="DIRECTORY"
                        if getattr(iofile, "is_directory", False)
                        else "FILE",
                    )
                )
                continue

            local_path = Path(os.path.abspath(str(iofile)))
            if self._is_within_workflow_root(local_path):
                if self.shared_workspace:
                    # In shared-workspace mode, skip uploading local inputs
                    # that are inside the workflow root — they are already
                    # visible on the shared PVC. Only upload storage-backed
                    # inputs (handled above).
                    continue
                if not self._is_source_path(local_path):
                    task_path = self._task_path_from_local_path(local_path)
                    input_type = (
                        "DIRECTORY"
                        if getattr(iofile, "is_directory", False) or local_path.is_dir()
                        else "FILE"
                    )
                    inputs.append(
                        tes.models.Input(
                            url=self._upload_local_input(job, local_path),
                            path=task_path,
                            type=input_type,
                        )
                    )
                continue
            raise WorkflowError(
                f"local input {local_path} is outside the workflow root and is not supported by the Conquest Snakemake executor"
            )

        return inputs

    def _build_outputs(self, job: JobExecutorInterface):
        outputs = []
        localizations: list[Localization] = []

        for iofile in job.output:
            entries, localization = self._output_entries(
                iofile, include_final=not getattr(iofile, "is_temp", False)
            )
            outputs.extend(entries)
            if localization is not None:
                localizations.append(localization)

        for iofile in job.log:
            entries, localization = self._output_entries(iofile, include_final=False)
            outputs.extend(entries)
            if localization is not None:
                localizations.append(localization)

        benchmark = getattr(job, "benchmark", None)
        if benchmark:
            entries, localization = self._output_entries(benchmark, include_final=False)
            outputs.extend(entries)
            if localization is not None:
                localizations.append(localization)

        return outputs, localizations

    def _output_entries(self, iofile, include_final: bool):
        task_path = self._task_path(iofile)
        rel = self._workflow_relative_path(iofile)
        output_type = "DIRECTORY" if getattr(iofile, "is_directory", False) else "FILE"

        workspace_url = s3.join_s3_url(self.workdir_uri, f"workspace/{rel}")
        outputs = [
            tes.models.Output(url=workspace_url, path=task_path, type=output_type)
        ]

        if getattr(iofile, "is_storage", False):
            query = getattr(iofile.storage_object, "query", "")
            if not str(query).startswith("s3://"):
                raise WorkflowError(
                    f"unsupported storage output {query!r}; only s3:// is supported"
                )
            outputs.append(
                tes.models.Output(url=str(query), path=task_path, type=output_type)
            )
        elif include_final:
            outputs.append(
                tes.models.Output(
                    url=s3.join_s3_url(self.output_dir_uri, rel),
                    path=task_path,
                    type=output_type,
                )
            )

        return outputs, Localization(
            url=workspace_url, path=str(iofile), type=output_type
        )

    def _task_path(self, iofile) -> str:
        rel = self._workflow_relative_path(iofile)
        return str(self.task_workflow_root / rel)

    def _task_path_from_local_path(self, local_path: Path) -> str:
        return str(self.task_workflow_root / local_path.relative_to(self.workflow_root))

    def _workflow_relative_path(self, iofile) -> str:
        raw = str(iofile)
        path = Path(raw)
        if not path.is_absolute():
            path = (self.workflow_workdir / path).resolve()
        else:
            path = path.resolve()
        if not self._is_within_workflow_root(path):
            raise WorkflowError(f"path {path} is outside the workflow root")
        return path.relative_to(self.workflow_root).as_posix()

    def _is_within_workflow_root(self, path: Path) -> bool:
        try:
            path.relative_to(self.workflow_root)
            return True
        except ValueError:
            return False

    def _is_source_path(self, local_path: Path) -> bool:
        relative = local_path.relative_to(self.workflow_root).as_posix()
        if relative in self.workflow_source_files:
            return True
        if local_path.is_dir():
            prefix = relative.rstrip("/") + "/"
            return any(path.startswith(prefix) for path in self.workflow_source_files)
        return False

    def _upload_local_input(self, job: JobExecutorInterface, local_path: Path) -> str:
        rel = local_path.relative_to(self.workflow_root).as_posix()
        digest = hashlib.sha256(rel.encode()).hexdigest()[:16]
        url = s3.join_s3_url(
            self.workdir_uri,
            f"task-assets/{job.jobid}/{digest}/{local_path.name}",
        )
        if local_path.is_dir():
            s3.upload_directory(self.s3_client, local_path, url)
        else:
            s3.upload_file(self.s3_client, local_path, url)
        return url

    def _localize_outputs(self, job_info: SubmittedJobInfo):
        localizations = [
            Localization(**entry) for entry in job_info.aux.get("localizations", [])
        ]
        for entry in localizations:
            destination = Path(entry.path)
            if not destination.is_absolute():
                destination = self.workflow_workdir / destination
            if entry.type == "DIRECTORY":
                s3.download_directory(self.s3_client, entry.url, destination)
            else:
                s3.download_file(self.s3_client, entry.url, destination)
