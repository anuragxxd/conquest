# Storage Plan

## Goal

Add a pluggable shared-workspace mode to Conquest so the platform can run in either:

- `emptydir` mode: current behavior, works on any cluster
- `pvc` mode: per-run shared PVC mounted into the runner pod and task pods

The key design rule is that Conquest should not depend on a specific storage backend. In `pvc` mode, the operator provides a Kubernetes `StorageClass`, and that backend can be CephFS, JuiceFS, NFS, EFS, democratic-csi, or anything else that satisfies the access pattern.

## Current State

Today the system uses ephemeral Kubernetes volumes:

- runner pods mount `emptyDir` at `/workspace` and `/tmp`, plus `/.nextflow` for Nextflow runners in `internal/backend/k8s/run_jobspec.go`
- task pods mount `emptyDir` for the computed workdir, plus `/stage` and `/tools`, in `internal/backend/k8s/jobspec.go`
- inputs are staged from S3 into local storage and outputs are uploaded back to S3

This is simple and portable, but it creates two important issues:

1. workflows cannot rely on a shared POSIX workspace across pods
2. Snakemake task-to-task local file dependencies are fragile because downstream tasks do not naturally see upstream task outputs unless they are explicitly localized through S3

## Proposed Design

Introduce a workspace mode abstraction:

- `emptydir` remains the default and preserves current behavior
- `pvc` creates one PVC per workflow run and mounts that same volume into:
  - the runner pod at `/workspace`
  - every task pod at the existing workdir mount points

Conquest still keeps S3 as the canonical artifact store for:

- run metadata
- logs
- final outputs
- storage layout versioning

This means the shared PVC solves the workspace and POSIX-visibility problem without forcing Conquest to change its storage layout model.

## Why This Is Cluster-Agnostic

The abstraction boundary is Kubernetes PVCs, not a specific filesystem product.

Conquest should say:

- "I need a shared workspace PVC for this run"

The cluster operator decides how that PVC is backed:

- CephFS
- JuiceFS
- NFS
- EFS
- local hostpath on a single-node cluster
- another RWX-capable CSI backend

If a cluster does not provide a suitable shared PVC backend, Conquest still works in `emptydir` mode.

## Configuration Changes

### `internal/config/config.go`

Add:

- `WorkspaceMode string` with values `emptydir` or `pvc`
- `WorkspaceStorageClass string`
- `WorkspaceSize string`

Environment variables:

- `CONQUEST_WORKSPACE_MODE`
- `CONQUEST_WORKSPACE_STORAGE_CLASS`
- `CONQUEST_WORKSPACE_SIZE`

Validation:

- reject unknown workspace modes
- require `CONQUEST_WORKSPACE_STORAGE_CLASS` when mode is `pvc`

### Helm values

Add to `deploy/charts/conquest/values.yaml`:

```yaml
workspace:
  mode: "emptydir"
  storageClass: ""
  size: "10Gi"
```

Add matching env vars in `deploy/charts/conquest/templates/deployment.yaml`.

## Domain and Persistence Changes

The run needs to remember which PVC belongs to it, and tasks need access to that PVC name when their Jobs are created.

### Database migration

Add a new migration, for example `migrations/000005_workspace_pvc.up.sql`, to add:

- `runs.workspace_pvc TEXT`
- `tasks.workspace_pvc TEXT`

### Domain structs

Update:

- `internal/domain/run.go` to include `WorkspacePVC string`
- `internal/domain/task.go` to include `WorkspacePVC string`

### Repository changes

Update Postgres repository code so `workspace_pvc` is:

- inserted
- selected
- scanned
- updated via a setter like `SetWorkspacePVC(...)`

## Runner Backend Changes

### `internal/backend/k8s/run_backend.go`

Extend `RunConfig` with:

- `WorkspaceMode`
- `WorkspaceStorageClass`
- `WorkspaceSize`

When `WorkspaceMode == "pvc"`:

1. create a per-run PVC before creating the runner Job
2. persist the PVC name on the `Run`
3. pass the PVC name into `buildRunnerJobSpec(...)`

When `WorkspaceMode == "emptydir"`, preserve current behavior.

### New helper file

Add `internal/backend/k8s/workspace.go` with helpers like:

- `CreateRunWorkspacePVC(...)`
- `DeleteRunWorkspacePVC(...)`

Expected PVC properties:

- one PVC per run
- labeled with the run id and a managed-by marker
- size from config
- storage class from config
- access mode chosen for shared use

Note: the exact access mode depends on the backend strategy. The intended production pattern is a shared filesystem backend. On single-node clusters, a single-node PVC can still be useful if the scheduler and backend allow the runner and tasks to mount it on that one node.

## Runner Job Spec Changes

### `internal/backend/k8s/run_jobspec.go`

Today, the runner workspace is:

- `emptyDir` mounted at `/workspace`

Change the implementation so:

- when a workspace PVC is present, mount the PVC at `/workspace`
- otherwise keep `emptyDir`

No runner code changes are required in:

- `internal/runner/runner.go`
- `internal/runner/snakemake.go`

Those runners already write to `/workspace`, so the backing volume can change transparently.

## Task Backend Changes

### `internal/backend/k8s/backend.go`

Ensure the task backend can pass the run-specific PVC name into the Job builder.

Recommended approach:

- copy `run.WorkspacePVC` onto `task.WorkspacePVC` when the task is created
- avoid making the task backend fetch runs directly

### `internal/api/tes/handlers.go`

When creating a task for a run:

- read the parent run
- copy `WorkspacePVC` from the run to the task

### `internal/backend/k8s/jobspec.go`

Today, task Jobs use `emptyDir` for the `workdir` volume.

Change the implementation so:

- when `task.WorkspacePVC` is set, mount that PVC as the `workdir` volume
- otherwise keep `emptyDir`

Keep the existing extra volumes as-is:

- `/stage` remains `emptyDir`
- `/tools` remains `emptyDir`

Keep the existing mount-path logic as-is:

- computed workspace mount path
- bucket-style secondary mount path when needed

This keeps the current path semantics stable while changing only the storage backing.

## Filer Behavior

### Phase 1

Do not optimize stage-in or stage-out yet.

Keep:

- `internal/filer/stagein.go`
- `internal/filer/stageout.go`
- `internal/filer/exec.go`

functionally unchanged.

Reason:

- shared PVC already fixes correctness by making files visible across pods
- keeping filer behavior stable reduces risk
- S3 remains the canonical artifact store

This means there may still be redundant transfers in early shared-workspace mode, but the behavior stays safe and easy to reason about.

### Phase 2

Later optimizations can include:

- skipping stage-in for files already present on the shared workspace
- skipping stage-out for workspace-only intermediate files
- reducing duplicate S3 traffic for Snakemake intermediate outputs

## Snakemake Changes

This is where the shared workspace gives the biggest benefit.

### `python/snakemake_executor_plugin_conquest_tes/__init__.py`

Add a flag such as `CONQUEST_SHARED_WORKSPACE=true` when `WorkspaceMode == "pvc"`.

Then update the plugin so that when shared workspace is enabled:

- it no longer behaves as if there is no shared filesystem
- it can rely on upstream outputs already being present in the shared workspace
- it can reduce or remove output re-localization from S3 for local workflow-root files

This should directly address the current Snakemake task-to-task dependency gap.

When shared workspace is not enabled, keep the current behavior.

## Run Cleanup

The run reconciler should delete the per-run PVC after the run reaches a terminal state.

### `internal/reconciler/run_reconciler.go`

When a run enters a terminal state:

- if `workspace_pvc` is set, attempt PVC deletion
- ignore `NotFound`
- log cleanup failures, but do not hide the run terminal state because of cleanup problems

Longer-term, a periodic cleanup pass could be added as a safety net for orphaned PVCs.

## Files Expected to Change

- `cmd/conquest/main.go`
- `internal/config/config.go`
- `internal/domain/run.go`
- `internal/domain/task.go`
- `internal/api/tes/handlers.go`
- `internal/backend/k8s/run_backend.go`
- `internal/backend/k8s/backend.go`
- `internal/backend/k8s/run_jobspec.go`
- `internal/backend/k8s/jobspec.go`
- `internal/backend/k8s/workspace.go` (new)
- `internal/repository/pg/runs.go`
- `internal/repository/pg/tasks.go`
- `internal/reconciler/run_reconciler.go`
- `internal/adapter/snakemake_run.go`
- `python/snakemake_executor_plugin_conquest_tes/__init__.py`
- `deploy/charts/conquest/values.yaml`
- `deploy/charts/conquest/templates/deployment.yaml`
- `migrations/000005_workspace_pvc.up.sql` (new)

## Files That Should Stay Stable in Phase 1

- `internal/storage/layout.go`
- `internal/runner/runner.go`
- `internal/runner/snakemake.go`
- `internal/filer/stagein.go`
- `internal/filer/stageout.go`
- `internal/filer/exec.go`
- `internal/adapter/nextflow.go`

## Backward Compatibility

Default behavior remains unchanged.

If the operator does nothing:

- Conquest stays in `emptydir` mode
- current deployments continue to work

If the operator enables `pvc` mode:

- the platform opts into shared workspace behavior
- the actual storage backend remains an operator concern, not a Conquest concern

## Testing Plan

### Unit tests

Add or extend tests for:

- `internal/backend/k8s/run_jobspec.go`
- `internal/backend/k8s/jobspec.go`
- new PVC helper logic in `internal/backend/k8s/workspace.go`
- repository scan and persistence of `workspace_pvc`

### Integration tests

1. single-node cluster test with PVC-backed workspace
2. multi-step Snakemake workflow where downstream tasks consume upstream local outputs
3. fallback test with `emptydir` mode to confirm current behavior is preserved

## Suggested Delivery Phases

### Phase 1: workspace plumbing

- config
- PVC lifecycle
- runner/task mounting
- persistence of `workspace_pvc`
- no filer optimization yet

### Phase 2: Snakemake shared-fs behavior

- shared-workspace flag
- plugin behavior cleanup
- task-to-task dependency validation

### Phase 3: transfer optimization

- reduce redundant stage-in/stage-out
- improve performance for shared-workspace mode

## Expected Outcome

After this change:

- Conquest remains portable by default
- operators can opt into shared workspaces without changing Conquest code again
- the platform no longer depends on a specific storage product choice
- Snakemake has a path to correct shared-filesystem semantics
- S3 remains the long-term artifact and metadata store
