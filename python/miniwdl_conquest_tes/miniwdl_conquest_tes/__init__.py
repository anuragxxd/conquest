"""
miniwdl container backend plugin that dispatches WDL tasks to a Conquest
internal TES endpoint.

Architecture
------------
The plugin runs inside a runner pod that shares a PersistentVolumeClaim with
the TES task pods.  Because both sides mount the same PVC at the shared
workspace root (typically ``/workspace``), the plugin sets
``container_dir = host_dir`` so that every path is identical on both the
orchestrator and the worker.  This eliminates the need for TES-level file
staging: inputs are copied into the work directory by miniwdl before
``_run()`` is called, and outputs are read directly from the PVC after the
TES task completes.

Configuration
-------------
The plugin reads two mandatory environment variables:

``CONQUEST_INTERNAL_TES_URL``
    Full URL of the run-scoped internal TES endpoint, e.g.
    ``http://conquest:8080/internal/runs/<run_id>/ga4gh/tes/v1``.

``CONQUEST_RUN_ID``
    The WES run identifier, used for logging.

Activate the plugin with::

    [scheduler]
    container_backend = conquest_tes
"""

from __future__ import annotations

import logging
import math
import os
import time
from contextlib import ExitStack
from typing import Any, Callable, Dict, Optional

import tes

from WDL.runtime import config
from WDL.Error import InputError
from WDL.runtime.error import Interrupted, Terminated
from WDL.runtime.task_container import TaskContainer

from . import s3stage

__all__ = ["ConquestTESContainer"]

# Default poll interval (seconds) between status checks against the TES API.
_DEFAULT_POLL_INTERVAL = 5.0

# Maximum poll interval (seconds) — used as a ceiling for backoff.
_MAX_POLL_INTERVAL = 30.0

# TES states that indicate a task is still in progress.
_ACTIVE_STATES = frozenset({"UNKNOWN", "QUEUED", "INITIALIZING", "RUNNING", "PAUSED"})

_PERMISSION_TOLERANT_CHMOD_INSTALLED = False


def _install_permission_tolerant_chmod() -> None:
    """Teach miniwdl to skip chmod failures on root-owned PVC entries.

    In the shared-PVC TES model, task containers can legitimately leave behind
    files/directories owned by root. miniwdl's post-task chmod pass is only a
    best-effort accessibility normalization step, so a permission error there
    should not convert an otherwise successful TES task into a failed workflow.
    """

    global _PERMISSION_TOLERANT_CHMOD_INSTALLED
    if _PERMISSION_TOLERANT_CHMOD_INSTALLED:
        return

    from WDL import StdLib as wdl_stdlib
    from WDL import _util as wdl_util
    from WDL.runtime import task as runtime_task

    def chmod_R_plus(path: str, file_bits: int = 0, dir_bits: int = 0) -> None:
        def do1(path1: str, bits: int) -> None:
            assert 0 <= bits < 0o10000
            if not os.path.islink(path1) and wdl_util.path_really_within(path1, path):
                try:
                    os.chmod(path1, (os.stat(path1).st_mode & 0o7777) | bits)
                except FileNotFoundError:
                    return
                except PermissionError:
                    return

        if os.path.isdir(path):
            do1(path, dir_bits)
            for root, subdirs, files in os.walk(path, followlinks=False):
                for dn in subdirs:
                    do1(os.path.join(root, dn), dir_bits)
                for fn in files:
                    do1(os.path.join(root, fn), file_bits)
        else:
            do1(path, file_bits)

    wdl_util.chmod_R_plus = chmod_R_plus
    runtime_task.chmod_R_plus = chmod_R_plus
    wdl_stdlib.chmod_R_plus = chmod_R_plus
    _PERMISSION_TOLERANT_CHMOD_INSTALLED = True


class ConquestTESContainer(TaskContainer):
    """
    TaskContainer backend that dispatches each WDL task as a GA4GH TES task
    to the Conquest internal TES API.

    Two staging modes:

    - shared workspace (default): the runner and task pods mount the same
      volume, paths are identical on both sides, and the TES task declares
      no inputs or outputs.
    - S3 (``CONQUEST_WDL_TASK_STAGING=s3``, runs without a shared
      workspace): the task declares its inputs, command, work directory and
      streams as TES inputs and outputs under ``CONQUEST_WDL_WORK_URI``, and
      the work directory is downloaded back afterwards. Such tasks need
      nothing but S3, so Conquest can also run them on remote TES endpoints.
    """

    # Class-level state set once by global_init().
    _resource_limits: Dict[str, int] = {}
    _tes_url: str = ""
    _s3_work_uri: str = ""
    _s3_client: Any = None
    _s3_index: Optional[s3stage.Index] = None

    # ------------------------------------------------------------------
    # Class methods
    # ------------------------------------------------------------------

    @classmethod
    def global_init(cls, cfg: config.Loader, logger: logging.Logger) -> None:
        _install_permission_tolerant_chmod()

        url = os.environ.get("CONQUEST_INTERNAL_TES_URL", "").strip()
        if not url:
            raise RuntimeError(
                "CONQUEST_INTERNAL_TES_URL must be set to the run-scoped "
                "internal TES endpoint URL"
            )
        cls._tes_url = url.rstrip("/")

        # Resource limits are effectively unbounded — the TES server and its
        # K8s backend handle actual scheduling.  We report generous limits so
        # miniwdl's scheduler does not artificially throttle task submission.
        cpu_limit = int(os.environ.get("CONQUEST_TES_MAX_CPU", "256"))
        mem_limit = int(os.environ.get("CONQUEST_TES_MAX_MEM_GB", "1024")) * (1024**3)
        cls._resource_limits = {"cpu": cpu_limit, "mem_bytes": mem_limit}

        staging = os.environ.get("CONQUEST_WDL_TASK_STAGING", "").strip()
        if staging == "s3":
            work_uri = os.environ.get("CONQUEST_WDL_WORK_URI", "").strip()
            if not work_uri.startswith("s3://"):
                raise RuntimeError("CONQUEST_WDL_WORK_URI must be an s3:// URI for S3 staging")
            cls._s3_work_uri = work_uri.rstrip("/")
            cls._s3_client = s3stage.make_client()
            cls._s3_index = s3stage.Index()
        elif staging:
            raise RuntimeError(f"unknown CONQUEST_WDL_TASK_STAGING {staging!r}")

        logger.info(
            "ConquestTES backend initialized "
            "(endpoint=%s, cpu_limit=%d, mem_gb_limit=%d)",
            cls._tes_url,
            cpu_limit,
            mem_limit // (1024**3),
        )

    @classmethod
    def detect_resource_limits(
        cls, cfg: config.Loader, logger: logging.Logger
    ) -> Dict[str, int]:
        if not cls._resource_limits:
            cls.global_init(cfg, logger)
        return cls._resource_limits

    # ------------------------------------------------------------------
    # Instance lifecycle
    # ------------------------------------------------------------------

    def __init__(self, cfg: config.Loader, run_id: str, host_dir: str) -> None:
        super().__init__(cfg, run_id, host_dir)
        if not self.s3_staging:
            # Shared PVC means identical paths on both the runner pod and the
            # task pod.  Setting container_dir = host_dir prevents any path
            # virtualisation, matching the approach used by miniwdl-aws with
            # EFS.  With S3 staging, miniwdl's own container_dir is kept: the
            # task sees its files where any miniwdl container would.
            self.container_dir = self.host_dir

        self._tes_client: Optional[tes.HTTPClient] = None
        self._poll_interval = float(
            os.environ.get("CONQUEST_TES_POLL_INTERVAL", str(_DEFAULT_POLL_INTERVAL))
        )

    # ------------------------------------------------------------------
    # TES client (lazy)
    # ------------------------------------------------------------------

    @property
    def tes_client(self) -> tes.HTTPClient:
        if self._tes_client is None:
            self._tes_client = tes.HTTPClient(
                url=self._tes_url,
                timeout=30,
            )
        return self._tes_client

    # ------------------------------------------------------------------
    # Input handling
    # ------------------------------------------------------------------

    @property
    def s3_staging(self) -> bool:
        return bool(self._s3_work_uri)

    def add_paths(self, host_paths) -> None:
        """Map input paths into the container.

        With S3 staging, inputs go under ``<container_dir>/in/`` rather than
        miniwdl's ``work/_miniwdl_inputs/``. The work directory is a TES
        DIRECTORY output, and servers mount inputs and outputs separately:
        Funnel merges all input mounts into their common ancestor and fails
        if that overlaps an output mount. So inputs and the command live in
        in/, and outputs in work/ and streams/, which are disjoint.
        Otherwise this is miniwdl's own add_paths, with the same collision
        handling.
        """
        if not self.s3_staging:
            super().add_paths(host_paths)
            return
        assert not self._running
        by_dir: Dict[str, set] = {}
        for host_path in host_paths:
            stripped = host_path.rstrip("/")
            if host_path not in self.input_path_map and stripped not in self.input_path_map:
                if not os.path.exists(stripped):
                    raise InputError("input path not found: " + host_path)
                by_dir.setdefault(os.path.dirname(stripped), set()).add(host_path)
        base = os.path.join(self.container_dir, "in")
        for paths in by_dir.values():
            subd = "0"
            for host_path in paths:
                container_path = os.path.join(base, subd, os.path.basename(host_path.rstrip("/")))
                if host_path.endswith("/"):
                    container_path += "/"
                if container_path in self.input_path_map_rev:
                    subd = str(len(self.input_path_map) + 1)
            for host_path in paths:
                container_path = os.path.join(base, subd, os.path.basename(host_path.rstrip("/")))
                if host_path.endswith("/"):
                    container_path += "/"
                assert container_path not in self.input_path_map_rev
                self.input_path_map[host_path] = container_path
                self.input_path_map_rev[container_path] = host_path

    def copy_input_files(self, logger: logging.Logger) -> None:
        """Copy inputs into the work directory on the shared PVC.

        This is required because the TES task pod accesses files through the
        PVC, not via Docker bind mounts.  The base-class implementation copies
        from original host paths into ``host_dir/work/_miniwdl_inputs/``.
        With S3 staging, inputs are TES inputs instead.
        """
        if self.s3_staging:
            return
        super().copy_input_files(logger)

    # ------------------------------------------------------------------
    # Core execution
    # ------------------------------------------------------------------

    def _run(
        self,
        logger: logging.Logger,
        terminating: Callable[[], bool],
        command: str,
    ) -> int:
        command_path = os.path.join(self.host_dir, "command")
        with open(command_path, "w", encoding="utf-8") as outfile:
            outfile.write(command)

        # Pre-create stream files with runner ownership so miniwdl can chmod
        # them after the TES task finishes even if the executor runs as root.
        for stream_path in (self.host_stdout_txt(), self.host_stderr_txt()):
            with open(stream_path, "a", encoding="utf-8"):
                pass

        if self.s3_staging:
            task = self._build_s3_task(logger, command)
        else:
            task = self._build_tes_task(command)

        logger.info("submitting TES task (image=%s)", task.executors[0].image)
        task_id: str = self.tes_client.create_task(task)
        logger.info("TES task submitted (id=%s)", task_id)

        if not self.s3_staging:
            return self._poll_until_terminal(logger, terminating, task_id)
        try:
            exit_code = self._poll_until_terminal(logger, terminating, task_id)
        except Exception:
            # keep what the task left (e.g. stderr) for the error report
            self._collect_s3_outputs(logger, best_effort=True)
            raise
        self._collect_s3_outputs(logger, best_effort=exit_code != 0)
        return exit_code

    # ------------------------------------------------------------------
    # TES task construction
    # ------------------------------------------------------------------

    def _build_tes_task(self, command: str) -> tes.Task:
        """Translate the current task state into a GA4GH TES Task."""
        image = self.runtime_values.get("docker", "ubuntu:22.04")
        env = dict(self.runtime_values.get("env", {}))

        work_dir = self.host_work_dir()
        command_path = os.path.join(self.host_dir, "command")
        stdout_path = self.host_stdout_txt()
        stderr_path = self.host_stderr_txt()

        # The command shell (default /bin/bash) is configured in miniwdl; we
        # replicate the same invocation pattern used by the built-in backends.
        shell = self.cfg.get("task_runtime", "command_shell")
        exec_command = (
            f"cd {_shell_quote(work_dir)} && "
            f"{shell} {_shell_quote(command_path)} "
            f"> {_shell_quote(stdout_path)} 2> {_shell_quote(stderr_path)}"
        )

        executor = tes.Executor(
            image=image,
            command=["bash", "-c", exec_command],
            workdir=work_dir,
            env=env if env else None,
        )

        resources = self._build_resources()

        tags: Dict[str, str] = {
            # The internal TES route auto-injects conquest_run_id and the
            # shared workspace scope, but we set the scope tag explicitly for
            # clarity and forward-compatibility.
            "conquest_workspace_scope": "shared",
        }

        return tes.Task(
            name=self.run_id,
            executors=[executor],
            resources=resources,
            tags=tags,
        )

    def _s3_attempt_uri(self) -> str:
        """S3 prefix of this task attempt: the run's WDL work URI, the task
        directory's path (unique per call) and the attempt number."""
        return s3stage.join(
            self._s3_work_uri,
            self.host_dir.strip("/"),
            f"attempt-{max(self.try_counter, 1)}",
        )

    def _build_s3_task(self, logger: logging.Logger, command: str) -> tes.Task:
        """The TES task of an attempt with S3 staging."""
        base = self._s3_attempt_uri()
        cdir = self.container_dir
        inputs = []
        for n, (host_path, container_path) in enumerate(sorted(self.input_path_map.items())):
            is_dir = host_path.endswith("/")
            name = os.path.basename(host_path.rstrip("/"))
            url = s3stage.upload_input(
                self._s3_client, self._s3_index, host_path,
                s3stage.join(base, "inputs", str(n), name) + ("/" if is_dir else ""),
            )
            inputs.append(tes.Input(
                url=url.rstrip("/") if is_dir else url,
                path=container_path.rstrip("/"),
                type="DIRECTORY" if is_dir else "FILE",
            ))
        logger.info("staged %d inputs through S3 under %s", len(inputs), base)
        command_path = os.path.join(cdir, "in", "command")
        inputs.append(tes.Input(path=command_path, content=command, type="FILE"))

        work = os.path.join(cdir, "work")
        streams = os.path.join(cdir, "streams")
        shell = self.cfg.get("task_runtime", "command_shell")
        exec_command = (
            f"mkdir -p {_shell_quote(work)} {_shell_quote(streams)} && cd {_shell_quote(work)} && "
            f"{shell} {_shell_quote(command_path)} "
            f"> {_shell_quote(os.path.join(streams, 'stdout.txt'))} "
            f"2> {_shell_quote(os.path.join(streams, 'stderr.txt'))}"
        )
        env = dict(self.runtime_values.get("env", {}))
        executor = tes.Executor(
            image=self.runtime_values.get("docker", "ubuntu:22.04"),
            command=["bash", "-c", exec_command],
            workdir=work,
            env=env if env else None,
        )
        outputs = [
            tes.Output(url=s3stage.join(base, "work"), path=work, type="DIRECTORY"),
            tes.Output(url=s3stage.join(base, "stdout.txt"), path=os.path.join(streams, "stdout.txt"), type="FILE"),
            tes.Output(url=s3stage.join(base, "stderr.txt"), path=os.path.join(streams, "stderr.txt"), type="FILE"),
        ]
        return tes.Task(
            name=self.run_id,
            executors=[executor],
            inputs=inputs,
            outputs=outputs,
            resources=self._build_resources(),
        )

    def _collect_s3_outputs(self, logger: logging.Logger, best_effort: bool) -> None:
        """Download the attempt's work directory and streams to the runner."""
        base = self._s3_attempt_uri()
        try:
            count = s3stage.download_work_dir(
                self._s3_client, self._s3_index, s3stage.join(base, "work"), self.host_work_dir()
            )
            for name, dest in (("stdout.txt", self.host_stdout_txt()), ("stderr.txt", self.host_stderr_txt())):
                s3stage.download_optional(self._s3_client, s3stage.join(base, name), dest)
            logger.info("collected %d files of the task work directory from %s", count, base)
        except Exception as exc:
            if not best_effort:
                raise
            logger.warning("could not collect the task's files from %s: %s", base, exc)

    def _build_resources(self) -> Optional[tes.Resources]:
        cpu = self.runtime_values.get("cpu")
        mem_bytes = self.runtime_values.get("memory_reservation", 0)
        mem_gb = math.ceil(mem_bytes / (1024**3)) if mem_bytes > 0 else None

        if cpu is None and mem_gb is None:
            return None

        return tes.Resources(
            cpu_cores=int(cpu) if cpu else None,
            ram_gb=float(mem_gb) if mem_gb else None,
        )

    # ------------------------------------------------------------------
    # Status polling
    # ------------------------------------------------------------------

    def _poll_until_terminal(
        self,
        logger: logging.Logger,
        terminating: Callable[[], bool],
        task_id: str,
    ) -> int:
        """Poll the TES task until it reaches a terminal state.

        Returns the executor exit code (0 for COMPLETE, extracted from the
        FULL view for EXECUTOR_ERROR).
        """
        with ExitStack() as cleanup:
            poll_stderr = cleanup.enter_context(self.poll_stderr_context(logger))
            entered_running = False
            poll_interval = self._poll_interval

            while True:
                time.sleep(poll_interval)

                if terminating():
                    self._cancel_task(logger, task_id)
                    raise Terminated()

                state = self._get_task_state(task_id)

                # Enter the "running" context once the task is actually executing.
                if state == "RUNNING" and not entered_running:
                    cleanup.enter_context(self.task_running_context())
                    entered_running = True

                # Tail stderr while the task is running.
                if entered_running:
                    poll_stderr()

                if state not in _ACTIVE_STATES:
                    # Final stderr poll to capture any trailing lines.
                    poll_stderr()
                    return self._resolve_exit_code(logger, task_id, state)

                # Gentle backoff: increase interval up to the ceiling.
                poll_interval = min(poll_interval * 1.1, _MAX_POLL_INTERVAL)

    def _get_task_state(self, task_id: str) -> str:
        """Fetch the current state of a TES task."""
        task = self.tes_client.get_task(task_id, view="MINIMAL")
        return task.state or "UNKNOWN"

    def _resolve_exit_code(
        self, logger: logging.Logger, task_id: str, state: str
    ) -> int:
        """Map a terminal TES state to a process exit code."""
        if state == "COMPLETE":
            logger.info("TES task completed successfully (id=%s)", task_id)
            return 0

        if state == "EXECUTOR_ERROR":
            exit_code = self._fetch_exit_code(task_id)
            logger.warning("TES task failed (id=%s, exit_code=%d)", task_id, exit_code)
            return exit_code

        # SYSTEM_ERROR, CANCELED, PREEMPTED — not the executor's fault.
        logger.error("TES task ended in state %s (id=%s)", state, task_id)
        raise Interrupted(f"TES task {state} (id={task_id})")

    def _fetch_exit_code(self, task_id: str) -> int:
        """Retrieve the executor exit code from the FULL view."""
        try:
            task = self.tes_client.get_task(task_id, view="FULL")
            if task.logs and task.logs[0].logs:
                code = task.logs[0].logs[0].exit_code
                if code is not None:
                    return int(code)
        except Exception:
            pass
        # Fallback: if exit code is unavailable, report generic failure.
        return 1

    def _cancel_task(self, logger: logging.Logger, task_id: str) -> None:
        """Best-effort cancellation of a running TES task."""
        try:
            self.tes_client.cancel_task(task_id)
            logger.info("TES task cancellation requested (id=%s)", task_id)
        except Exception as exc:
            logger.warning("failed to cancel TES task (id=%s): %s", task_id, exc)


def _shell_quote(s: str) -> str:
    """Quote a string for safe interpolation in a shell command."""
    return "'" + s.replace("'", "'\\''") + "'"
