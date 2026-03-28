# Storage Plan — Shared Workspace for Conquest

## Goal

Support three workspace modes in Conquest while keeping S3 as the canonical
artifact store:

- **emptydir** — current default, works on any cluster
- **pvc + RWX** — per-run shared PVC mounted into runner and task pods on any node
- **pvc + RWO** — per-run shared PVC, all pods for a run co-located on one node

The operator provides a Kubernetes `StorageClass`; the actual backend can be
CephFS, JuiceFS, NFS, EFS, democratic-csi, or anything else that satisfies the
access pattern. Conquest never depends on a specific storage product.

## Architecture Decision

Workspace is a backend execution concern, not an engine adapter concern.

- Do **not** reuse the engine adapter pattern (`internal/adapter/`) for storage.
  Those adapters are engine planners, not infrastructure adapters.
- Do **not** put PVC/backend logic into `internal/storage/layout.go`. That file
  is S3 naming/layout only.
- Do **not** overload `BackendState` for workspace identity. Both reconcilers
  overwrite it with pod/status blobs.

The clean abstraction is a backend-local workspace strategy/provider under
`internal/backend/k8s/workspace.go`.

## Current State

Today the system uses ephemeral Kubernetes volumes:

- Runner pods mount `emptyDir` at `/workspace` and `/tmp`, plus `/.nextflow`
  for Nextflow runners (`internal/backend/k8s/run_jobspec.go`).
- Task pods mount `emptyDir` for the computed workdir, plus `/stage` and
  `/tools` (`internal/backend/k8s/jobspec.go`).
- Inputs are staged from S3 into local storage; outputs are uploaded back to S3
  via the in-pod filer wrapper (`internal/filer/exec.go`).

This is simple and portable but creates two issues:

1. Workflows cannot rely on a shared POSIX workspace across pods.
2. Snakemake task-to-task local file dependencies are fragile because downstream
   tasks do not naturally see upstream task outputs unless they are explicitly
   localized through S3. The real-world failure: a bioinformatics Snakemake
   workflow passed early steps, then failed because `var_call` expected
   `reference/MN908947.3.fasta.fai` to exist locally without declaring it as an
   input.

## Proposed Design

### PVC layout inside the claim

One PVC per run. Inside that claim, use logical subpaths:

```
<pvc-root>/
  shared/           # runner-visible workspace, mounted at /workspace in runner
  tasks/
    <taskID>/       # per-task isolated workspace (default for non-shared tasks)
```

- **Runner** mounts the PVC with `subPath: shared` at `/workspace`.
- **Isolated tasks** (default, including all Nextflow tasks) mount with
  `subPath: tasks/<taskID>` at the computed workdir mount path. This keeps
  current per-task isolation semantics.
- **Shared-scope tasks** (Snakemake shared mode) mount with
  `subPath: shared/workflow` (or similar) at `/workspace/workflow`. This aligns
  task absolute paths with the runner checkout so upstream outputs are visible.

This avoids cross-task write races for Nextflow while giving Snakemake real
shared-filesystem semantics.

### S3 remains canonical

Conquest still keeps S3 as the canonical artifact store for:

- Run metadata
- Logs
- Final outputs
- Storage layout versioning

The shared PVC solves the workspace and POSIX-visibility problem without forcing
Conquest to change its storage layout model.

## Configuration

### `internal/config/config.go`

Add:

```go
WorkspaceMode        string // "emptydir" or "pvc"
WorkspaceAccessMode  string // "RWX" or "RWO"
WorkspaceStorageClass string // optional; empty = cluster default
WorkspaceSize        string // e.g. "10Gi"
```

Environment variables:

- `CONQUEST_WORKSPACE_MODE` (default `emptydir`)
- `CONQUEST_WORKSPACE_ACCESS_MODE` (default `RWX`)
- `CONQUEST_WORKSPACE_STORAGE_CLASS` (optional)
- `CONQUEST_WORKSPACE_SIZE` (default `10Gi`)

Validation:

- Reject unknown workspace modes.
- `pvc` mode requires a valid `accessMode` (`RWX` or `RWO`).
- `storageClass` is optional; leaving it empty uses the Kubernetes cluster
  default. This is the Kubernetes-native pattern.
- `size` required or defaulted.
- `emptydir` ignores all PVC fields.

### `cmd/conquest/main.go`

Wire new config fields into `RunConfig` and `Config` for both task and run
backends.

### Helm values

Add to `deploy/charts/conquest/values.yaml`:

```yaml
workspace:
  mode: "emptydir"
  accessMode: "RWX"
  storageClass: ""
  size: "10Gi"
```

Add matching env vars in `deploy/charts/conquest/templates/deployment.yaml`.

## Domain Model

### New file: `internal/domain/workspace.go`

```go
type WorkspaceKind string

const (
    WorkspaceKindNone WorkspaceKind = "none"
    WorkspaceKindPVC  WorkspaceKind = "pvc"
)

type WorkspaceAccessMode string

const (
    WorkspaceAccessModeRWX WorkspaceAccessMode = "RWX"
    WorkspaceAccessModeRWO WorkspaceAccessMode = "RWO"
)

type WorkspaceBinding struct {
    Kind       WorkspaceKind       `json:"kind"`
    ClaimName  string              `json:"claim_name,omitempty"`
    AccessMode WorkspaceAccessMode `json:"access_mode,omitempty"`
}
```

### `internal/domain/run.go`

Add `Workspace *WorkspaceBinding` field.

### `internal/domain/task.go`

Add `Workspace *WorkspaceBinding` field.

## Persistence

### Database migration

Add `migrations/000005_workspace_binding.up.sql`:

```sql
BEGIN;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS workspace_kind TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS workspace_claim_name TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS workspace_access_mode TEXT;

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS workspace_kind TEXT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS workspace_claim_name TEXT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS workspace_access_mode TEXT;
COMMIT;
```

Add corresponding down migration.

### Repository changes

Update `internal/repository/interfaces.go`:

- Add `SetWorkspaceBinding(ctx, runID, binding)` to `RunStore`.

Update `internal/repository/pg/runs.go`:

- Insert, select, and scan the three new columns.
- Implement `SetWorkspaceBinding`.

Update `internal/repository/pg/tasks.go`:

- Insert, select, and scan the three new columns.
- Task binding is persisted via normal `Create(...)` — no extra setter needed
  initially.

## Backend Submission Contract

### `internal/backend/interfaces.go`

Extend `RunSubmission`:

```go
type RunSubmission struct {
    BackendRef string
    Workspace  *domain.WorkspaceBinding // nil for emptydir
}
```

`SubmitRun(...)` returns both `BackendRef` and `WorkspaceBinding`.

## K8s Workspace Strategy

### New file: `internal/backend/k8s/workspace.go`

Backend-local strategy factory maps config to one of three strategies:

```
EmptyDirWorkspace
PVCWorkspaceRWX
PVCWorkspaceRWO
```

Responsibilities:

- **Provision**: create per-run PVC (pvc modes only).
- **Runner volumes**: return volume source and mount for the runner job.
- **Task volumes**: return volume source, mount, and subPath for task jobs.
- **Placement**: return pod affinity constraints for RWO mode.
- **Runner env**: inject `CONQUEST_SHARED_WORKSPACE=true` for pvc modes.
- **Cleanup**: delete per-run PVC.

PVC properties:

- One PVC per run.
- Deterministic name: `conquest-workspace-<runID>` (truncated/sanitized for
  DNS).
- Labels: `conquest.io/run-id`, `app.kubernetes.io/managed-by: conquest`,
  `conquest.io/workspace-mode`.
- Access mode from strategy.
- Storage class from config (omitted if empty = cluster default).
- Size from config.

### EmptyDirWorkspace

Preserve current behavior exactly. No PVC creation. No scheduling changes.
Returns `emptyDir` volume sources for both runner and task mounts.

### PVCWorkspaceRWX

- Create one PVC per run with `ReadWriteMany`.
- Mount PVC `subPath: shared` into runner at `/workspace`.
- Mount PVC `subPath: tasks/<taskID>` into isolated task pods (default).
- Mount PVC `subPath: shared` into shared-scope task pods.
- No special scheduling needed.

### PVCWorkspaceRWO

- Create one PVC per run with `ReadWriteOnce`.
- Mount PVC `subPath: shared` into runner at `/workspace`.
- Same task mounting as RWX.
- **Placement**: add required pod affinity to all task jobs for this run:
  - match runner pod labels `conquest.io/run-id=<runID>` and
    `conquest.io/job-type=runner`
  - topology key `kubernetes.io/hostname`
- This ensures all task pods land on the same node as the runner without
  depending on reconciler-persisted `nodeName`. The runner pod is alive while it
  is orchestrating tasks.
- Document that `RWO` assumes a storage driver that supports same-node
  multi-pod mounts and that `WaitForFirstConsumer` binding mode is strongly
  preferred.

## Runner Mount Behavior

### `internal/backend/k8s/run_backend.go`

- For pvc modes: create workspace PVC before creating the runner Job.
- Persist `WorkspaceBinding` on the run via `RunStore.SetWorkspaceBinding(...)`.
- Pass workspace info into `buildRunnerJobSpec(...)`.
- On runner Job creation failure after PVC creation: delete PVC best-effort.

### `internal/backend/k8s/run_jobspec.go`

- Keep `/workspace` mount path stable.
- Keep `/tmp` as `emptyDir`.
- Keep `/.nextflow` as `emptyDir`.
- For pvc modes: mount the claim with `subPath: shared` at `/workspace`.
- For emptydir: keep current `emptyDir` volume source.
- Inject `CONQUEST_SHARED_WORKSPACE=true` env var for pvc modes.

No runner code changes in `internal/runner/runner.go` or
`internal/runner/snakemake.go`. Both already write to `/workspace`, so the
backing volume changes transparently.

## Task Inheritance

### `internal/api/tes/handlers.go`

- `CreateTaskForRun`: load the full run (not just `GetStatus(...)`), copy
  `run.Workspace` onto the new `task.Workspace`.
- Public `CreateTask`: also load the full run if `conquest_run_id` is provided
  in tags; copy workspace binding.
- This keeps task backends ignorant of run lookups.

## Task Mount Behavior

### `internal/backend/k8s/jobspec.go`

Keep current mount-path computation (`taskWorkspaceMountPath`) and executor
semantics unchanged.

Add a task workspace scope check from TES tags:

- default: `isolated`
- `conquest_workspace_scope=shared`: shared

For pvc modes:

- **Isolated** (default): mount claim `subPath: tasks/<taskID>` at the computed
  workdir mount path. Same isolation semantics as today's `emptyDir`.
- **Shared**: mount claim `subPath: shared` at a path that aligns with the
  runner checkout. For Snakemake this means `/workspace/workflow`.

Keep `/stage` and `/tools` as `emptyDir` always. Do not put staging or tool
volumes on the shared PVC.

Keep `DiskGb` mapping to ephemeral storage requests unchanged. PVC-backed
workspace size is a separate concern.

## Filer Behavior

### Phase 1a + 1b

Do not optimize stage-in or stage-out yet. Keep:

- `internal/filer/stagein.go`
- `internal/filer/stageout.go`
- `internal/filer/exec.go`

functionally unchanged.

Shared PVC already fixes correctness by making files visible across pods.
Keeping filer behavior stable reduces risk. S3 remains the canonical artifact
store.

There may still be redundant S3 transfers in shared-workspace mode. That is
acceptable for now and addressed in a later transfer optimization phase.

## Run Cleanup

### `internal/reconciler/run_reconciler.go`

When a run enters a terminal state:

- If `run.Workspace.Kind == pvc`, attempt PVC deletion via the workspace
  strategy.
- Ignore `NotFound`.
- Log cleanup failures but do not hide the terminal run state.
- Kubernetes PVC protection handles active mounts (PVC deletion is deferred
  until all pods unmount).

A periodic cleanup pass for orphaned PVCs can be added later as a safety net.

## RBAC

### `deploy/charts/conquest/templates/rbac.yaml`

Add to the Role rules:

```yaml
- apiGroups: [""]
  resources: ["persistentvolumeclaims"]
  verbs: ["create", "get", "list", "watch", "delete"]
```

## Snakemake Shared-Workspace Mode (Phase 1b)

### Backend-injected capability flag

`internal/backend/k8s/run_jobspec.go` injects
`CONQUEST_SHARED_WORKSPACE=true` for pvc modes. No adapter changes needed.
`internal/runner/snakemake.go` already uses `os.Environ()`, so the flag reaches
the Snakemake subprocess automatically.

### Plugin changes: `python/snakemake_executor_plugin_conquest_tes/__init__.py`

Add shared-workspace detection:

```python
shared_workspace = os.environ.get("CONQUEST_SHARED_WORKSPACE") == "true"
```

#### Emptydir path (unchanged)

- `task_root = /work`
- `task_workflow_root = /work/workflow`
- Bundle input included in TES task.
- Local inputs uploaded to S3.
- Outputs localized back from S3 after task completion.

#### Shared-workspace path (new)

- `task_root = /workspace`
- `task_workflow_root = /workspace/workflow`
- This aligns task absolute paths with the runner checkout so upstream outputs
  are visible at the same paths.
- Set TES tag `conquest_workspace_scope=shared` so the backend mounts the
  shared PVC subtree.
- **Do not** add the workflow bundle TES input. The runner checkout is already
  on the shared PVC.
- **Do not** upload local inputs that are inside the workflow root. Rely on the
  shared PVC for source files, upstream generated outputs, and implicit
  sidecars like `.fai`.
- **Keep** S3 upload for storage-backed inputs and anything outside the workflow
  root.
- **Keep** output uploads to S3 through the existing filer path. Declared
  outputs still upload to S3 (S3 stays canonical).
- **Skip** `_localize_outputs(...)` — outputs already exist locally on the
  shared PVC.

#### `implies_no_shared_fs`

Do not flip this setting immediately. Implement the capability flag first and
make correctness depend on path alignment and shared-scope mounting, not on
this upstream Snakemake setting. Optionally switch it under the same flag only
after the real workflow passes end-to-end.

This is the highest-risk Snakemake behavior toggle because it changes
scheduler/executor assumptions upstream of the plugin.

## Nextflow Compatibility

Nextflow is not affected by this change:

- `internal/adapter/nextflow.go` stays unchanged; `workDir` remains S3.
- `internal/runner/runner.go` only uses `/workspace` for local config/log
  scratch.
- Nextflow task pods keep isolated task subpaths on the PVC (`tasks/<taskID>`),
  so they do not share live local task files with each other.
- TES API surface does not change. Task `volumes` are still rejected.
- No adapter, runner, or filer code changes are needed for Nextflow.

## Snakemake Emptydir Compatibility

Snakemake emptydir mode is not affected:

- The backend does not inject `CONQUEST_SHARED_WORKSPACE` in emptydir mode.
- The plugin keeps current `/work` behavior when the flag is absent.
- The runner env contract in `internal/runner/snakemake.go` stays unchanged.
- Bundle upload and S3 localization continue as-is.

## Files Expected to Change

### Phase 1a — Workspace Plumbing

- `cmd/conquest/main.go`
- `internal/config/config.go`
- `internal/domain/workspace.go` (new)
- `internal/domain/run.go`
- `internal/domain/task.go`
- `internal/repository/interfaces.go`
- `internal/repository/pg/runs.go`
- `internal/repository/pg/tasks.go`
- `internal/backend/interfaces.go`
- `internal/backend/k8s/workspace.go` (new)
- `internal/backend/k8s/workspace_test.go` (new)
- `internal/backend/k8s/run_backend.go`
- `internal/backend/k8s/run_jobspec.go`
- `internal/backend/k8s/run_jobspec_test.go`
- `internal/backend/k8s/jobspec.go`
- `internal/backend/k8s/jobspec_test.go`
- `internal/api/tes/handlers.go`
- `internal/reconciler/run_reconciler.go`
- `deploy/charts/conquest/templates/rbac.yaml`
- `deploy/charts/conquest/values.yaml`
- `deploy/charts/conquest/templates/deployment.yaml`
- `migrations/000005_workspace_binding.up.sql` (new)
- `migrations/000005_workspace_binding.down.sql` (new)

### Phase 1b — Snakemake Shared-FS Behavior

- `internal/backend/k8s/run_jobspec.go` (add env injection)
- `python/snakemake_executor_plugin_conquest_tes/__init__.py`

## Files That Must Stay Stable

- `internal/storage/layout.go`
- `internal/objstore/interfaces.go`
- `internal/filer/stagein.go`
- `internal/filer/stageout.go`
- `internal/filer/exec.go`
- `internal/runner/runner.go`
- `internal/runner/snakemake.go`
- `internal/adapter/nextflow.go`
- `internal/adapter/nextflow_run.go`
- `internal/adapter/snakemake_run.go`
- `internal/adapter/registry.go`

## Testing Plan

### Unit tests (Phase 1a)

- `internal/backend/k8s/workspace_test.go`:
  - PVC spec generation for RWX and RWO.
  - Runner/task subPath mapping.
  - RWO pod affinity generation.
  - EmptyDir passthrough.
- `internal/backend/k8s/run_jobspec_test.go`:
  - Emptydir runner mount unchanged.
  - PVC runner mount uses claim + `subPath: shared`.
  - `/tmp` and `/.nextflow` stay `emptyDir`.
  - `CONQUEST_SHARED_WORKSPACE` env injected for pvc modes.
- `internal/backend/k8s/jobspec_test.go`:
  - Isolated PVC task mount uses `subPath: tasks/<taskID>`.
  - Shared-scope PVC task mount maps to `subPath: shared/...`.
  - Emptydir behavior unchanged.
- Repository tests for new workspace binding columns.
- Handler tests: task inherits run workspace binding.
- Reconciler tests: terminal run triggers workspace cleanup.

### Integration tests (Phase 1b)

1. Re-run the known failing Snakemake workflow in emptydir — confirm same
   current behavior.
2. Re-run in pvc + RWX — confirm the implicit `.fai` sidecar case works.
3. Re-run in pvc + RWO on a single-node or same-node-capable cluster.
4. Run a multi-step Nextflow workflow in all three modes — confirm no
   regressions.

## Delivery Phases

### Phase 1a: Workspace Plumbing

- Config
- Domain model
- Workspace binding persistence
- Backend submission contract
- K8s workspace strategy/provider
- Runner/task mount behavior
- Task inheritance
- RWO placement via pod affinity
- Run cleanup
- RBAC
- Tests

### Phase 1b: Snakemake Shared-FS Behavior

- Capability flag injection
- Plugin behavior change
- End-to-end validation with real workflow

### Phase 2: Transfer Optimization (future)

- Skip stage-in for files already present on the shared workspace.
- Skip stage-out for workspace-only intermediate files.
- Reduce duplicate S3 traffic for Snakemake intermediate outputs.

## Operational Notes

### Shared PVC permissions

Runner and task Jobs currently set no pod `securityContext`. With a shared PVC,
different images may fail to read or write each other's files due to UID/GID
mismatch. Consider adding optional `fsGroup` config for spawned jobs in a
follow-up. Do not force `runAsUser` in phase 1.

### RWO caveats

- Kubernetes allows same-node multi-pod RWO in principle, but CSI driver
  behavior matters.
- `WaitForFirstConsumer` binding mode is strongly preferred.
- Document that `RWO` assumes a driver/class that supports this pattern.
- If the cluster cannot support multi-pod same-node RWO, reject or disable
  this mode.

### `storageClass` optional

Leaving `storageClass` empty uses the Kubernetes cluster default. This is the
most Kubernetes-native pattern. Make it optional, but document that RWO often
needs an explicitly suitable class.

## Anti-Patterns to Avoid

- Do not put PVC/backend logic into `internal/storage/layout.go`.
- Do not create a new storage adapter under `internal/adapter/`; wrong layer.
- Do not overload `BackendState` for workspace identity.
- Do not treat RWX and RWO as the same implementation.
- Do not scatter `if workspaceMode == ...` across runners, plugin, filer, and
  backends; centralize in the workspace strategy.
- Do not put `/stage` or `/tools` on the shared PVC.
- Do not make S3 optional; PVC is execution workspace, not source of truth.
- Do not make RWO correctness depend on reconciler-persisted `nodeName`; use
  pod affinity to runner labels instead.
- Do not flip Snakemake `implies_no_shared_fs` before the real workflow passes.

## Backward Compatibility

Default behavior remains unchanged.

If the operator does nothing:

- Conquest stays in `emptydir` mode.
- Current deployments continue to work.

If the operator enables `pvc` mode:

- The platform opts into shared workspace behavior.
- The actual storage backend remains an operator concern, not a Conquest concern.

## Expected Outcome

After this change:

- Conquest remains portable by default.
- Operators can opt into shared workspaces without changing Conquest code again.
- The platform no longer depends on a specific storage product choice.
- Snakemake has correct shared-filesystem semantics for task-to-task
  dependencies.
- S3 remains the long-term artifact and metadata store.
- Nextflow behavior is unaffected.
- All three modes are supported from day one.
