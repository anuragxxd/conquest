# Conquest

Workflow execution service. Runs Nextflow, WDL, and Snakemake
pipelines on Kubernetes through GA4GH-standard APIs.

Conquest implements a [WES](https://ga4gh.github.io/workflow-execution-service-schemas/) (Workflow Execution Service) control plane
with an internal [TES](https://ga4gh.github.io/task-execution-schemas/) (Task Execution Service) backend. A single Go binary
handles all roles — API server, workflow runner, and task file staging —
depending on how it's invoked.

## Architecture

```
                           +----------+
                           |  Client  |
                           +----+-----+
                                |
                           WES API v1.1
                                |
  +-----------+     +-----------v----------+     +----------+
  |  Adapter  |<----|    Run Controller    |---->| Postgres |
  |  Registry |     |                      |<----|  (state) |
  |  NF / SMK |     +-----------+----------+     +----^-----+
  +-----------+                 |                      |
                                | K8s Job              | poll
                      +---------v----------+     +-----+------+
                      |    Runner Pod      |     | Reconciler |
                      | (NF / WDL / SMK)   |     | (K8s > DB) |
                      +---------+----------+     +------------+
                                |
                         TES v1.1 (internal)
                                |
                      +---------v----------+
                      |   Task K8s Jobs    |
                      |                    |
                      |  init: stage-in    |
                      |  main: executor    |
                      |  post: stage-out   |
                      +---------+----------+
                                |
                      +---------v----------+
                      |     S3 / MinIO     |
                      |   (object store)   |
                      +--------------------+
```

**How a workflow runs:**

1. Client submits a workflow via the **WES API**.
2. The **Run Controller** picks the right engine adapter, persists the run in
   Postgres, and creates a **Runner Job** on Kubernetes.
3. The runner executes the workflow engine (Nextflow, WDL, or Snakemake), which
   submits individual tasks back to Conquest's **internal TES API**.
4. Each task becomes a **K8s Job** — an init container stages inputs from S3,
   the executor runs the user command, then outputs are uploaded back to S3.
5. **Reconcilers** poll Kubernetes in the background and sync Job/Pod state
   back to Postgres.

Postgres is the system of record. S3 is the data plane. Kubernetes is the
compute layer.

## Supported Engines

| Engine    | Version  | Default | Execution Mode                                       |
|-----------|----------|---------|------------------------------------------------------|
| Nextflow  | 24.10.4  | Yes     | TES dispatch via [`nf-ga4gh`][nf-ga4gh] plugin       |
| Nextflow  | 25.04.3  |         | TES dispatch via [`nf-ga4gh`][nf-ga4gh] plugin       |
| Nextflow  | 25.10.2  |         | TES dispatch via [`nf-ga4gh`][nf-ga4gh] plugin       |
| Nextflow  | 25.10.4  |         | TES dispatch via [`nf-ga4gh`][nf-ga4gh] plugin       |
| WDL       | 1.12.1   | Yes     | [`miniwdl`][miniwdl] runner with TES-dispatch container backend |
| Snakemake | 9.17.3   | Yes     | TES dispatch via `conquest-tes` executor plugin      |
| Snakemake | 7.32.4   |         | Local execution in runner pod                        |

[nf-ga4gh]: https://github.com/nextflow-io/nf-ga4gh
[miniwdl]: https://miniwdl.readthedocs.io/

Nextflow versions use the upstream [`nf-ga4gh`][nf-ga4gh] plugin to submit
tasks to Conquest's TES API.

WDL uses a custom [`miniwdl`][miniwdl] container backend plugin (`python/miniwdl_conquest_tes`)
which dispatches every WDL task to Conquest's internal TES API. The WDL engine
name is `wdl`, and the current supported WDL language versions are `1.0` and
`1.1`.

The `conquest-tes` executor plugin (under `python/` in this repo) is a custom
Snakemake executor based on the upstream
[`snakemake-executor-plugin-tes`](https://github.com/snakemake/snakemake-executor-plugin-tes),
adapted for Conquest's internal TES API and S3 staging conventions.

Snakemake 7.x predates the executor plugin system
([introduced in 8.0](https://snakemake.readthedocs.io/en/stable/getting_started/migration.html)),
so the `conquest-tes` plugin cannot be used. The entire workflow runs locally
inside the runner pod rather than being broken into individual TES tasks.

Each engine version gets its own Docker image with pinned dependencies. The
version is selected at submission time via `workflow_engine_version` in the WES
request — if omitted, the default is used.

## Task Resource Control

All supported engines dispatch work through TES, so users control CPU, memory,
and disk for each task pod directly in their workflow. Nextflow uses process
directives (`cpus`, `memory`, `disk`); WDL uses task runtime attributes such as
`cpu`, `memory`, `disks`/`disk`, and `docker`; Snakemake uses rule resources
(`cpus`, `mem_gb`, `disk_gb`). The engine plugin translates these to TES
resource fields, and Conquest maps them to Kubernetes resource requests/limits.

A task policy layer fills in defaults when omitted (1 CPU, 2 GB RAM, 8 GB
disk) and enforces platform-level floors and ceilings. See
`CONQUEST_TASK_DEFAULT_*`, `CONQUEST_TASK_MIN_*`, and `CONQUEST_TASK_MAX_*`
in [`internal/config/config.go`](internal/config/config.go).

## Workspace Modes

Controls how the runner pod and task pods share (or don't share) a filesystem.

| Mode          | Volume Type          | Shared Filesystem | Notes                                                                                                     |
|---------------|----------------------|-------------------|-----------------------------------------------------------------------------------------------------------|
| `objectfs`    | S3 via FUSE          | Yes               | Run workspace is an S3 prefix mounted into every pod. No block storage needed. See below.                  |
| `pvc` + `RWX` | PVC (ReadWriteMany)  | Yes               | Requires NFS or a similar StorageClass.                                                                   |
| `pvc` + `RWO` | PVC (ReadWriteOnce)  | Yes               | Good fallback when NFS isn't available. Co-locates tasks on the same node as the runner via pod affinity. |
| `emptydir`    | Kubernetes emptyDir  | No                | Each task stages inputs from S3 and uploads outputs back to S3 independently. Fully compatible with Nextflow and simple Snakemake runs. Good fallback when the cluster has no persistent storage provisioner. |

```bash
CONQUEST_WORKSPACE_MODE=objectfs     # "emptydir", "pvc", or "objectfs"
CONQUEST_WORKSPACE_ACCESS_MODE=RWX   # "RWX" or "RWO" (pvc only)
CONQUEST_WORKSPACE_SIZE=10Gi
```

### `objectfs` — S3 as the workspace filesystem

`objectfs` gives the runner and every task pod one shared POSIX filesystem
that *is* an S3 prefix. It needs no NFS, no RWX StorageClass and no block
storage, which makes it the mode that works on clusters where the other
shared options are unavailable.

**How it is put together.** Conquest creates a static `PersistentVolume` per
run whose `volumeHandle` is the run's own S3 prefix, plus a matching PVC:

```
/workspace/results/a.bam
  -> s3://<bucket>/v1/workflow-runs/<runID>/workspace/results/a.bam
```

The mount is performed by [GeeseFS][geesefs] (Apache 2.0) through the
[Yandex S3 CSI driver][csi-s3] (Apache 2.0). Both are implementation detail:
the only user-facing surface is `CONQUEST_WORKSPACE_MODE=objectfs`.

The layout is deliberately transparent — one object per file, human-readable
keys, no chunking or opaque metadata store. Anything written through the
mount can be read with `mc`, the AWS SDK or the MinIO console, and anything
uploaded by other means appears in the mount. This is why JuiceFS and
similar chunk-plus-metadata designs were rejected despite better POSIX
fidelity.

**Mount options and why each is required** (`internal/backend/k8s/workspace.go`):

| Flag | Why |
|---|---|
| `--no-systemd` | Talos has no systemd; GeeseFS must not try to register a unit. |
| `--dir-mode 0777` / `--file-mode 0666` | Fallback modes for objects with no stored mode. |
| `--enable-perms` | Stores real modes as `x-amz-meta-mode`. Without it `chmod()` is silently discarded and executable bits on `bin/` scripts are lost. |
| `--enable-mtime` | Preserves mtime. Without it miniwdl dies in `bump_atime` with `EPERM`, and Snakemake's mtime-driven DAG logic is unreliable. |
| `--enable-specials` | Enables named pipes. GeeseFS only auto-enables this for endpoints containing `yandex`, so it must be explicit on MinIO. Without it STAR cannot create its FIFO and every `STAR_ALIGN` task fails. |

The last three all carry the same upstream caveat — they need the S3 backend
to return `UserMetadata` in listings. MinIO does; verified by round-tripping
mode, mtime and file type across a remount, at no measurable listing cost.

Conquest also clears the process `umask` in the runner and in every task
executor when the workspace is shared (`internal/sharedfs`). The runner is
typically root while task images often run as a non-root UID; with the
default `umask 022` the runner's files land `0644`/`0755` and tasks get
`EACCES`. See that package for the full reasoning.

**Known limitations.** These are properties of S3-backed FUSE, not bugs:

- No hard links (`ENOTSUP`). Harmless here — the WDL runner sets
  `output_hardlinks = false`, so miniwdl uses symlinks, which do work.
- No "invisible" deleted files: unlinking a file that is still open yields
  `ESTALE`, so `tempfile.NamedTemporaryFile(delete=True)` patterns break.
- Concurrent writes to the same object from multiple pods are unsafe.
- Advisory locks (`flock`, `fcntl` `F_SETLK`) are handled locally by the
  kernel per mount, so they serialise within a pod but not across pods.

**Cleanup.** When a run reaches a terminal state Conquest deletes the run's
Jobs, then the PVC and PV. **S3 objects are deliberately retained** — data
lifecycle is separate from workspace lifecycle. The Job deletion has to come
first: `pvc-protection` blocks PVC deletion while any Pod still *references*
the claim, including Pods that have already completed.

[geesefs]: https://github.com/yandex-cloud/geesefs
[csi-s3]: https://github.com/yandex-cloud/k8s-csi-s3

## GA4GH APIs

Conquest implements both GA4GH v1.1 specifications.

**WES** is the external-facing API for submitting and managing workflow runs:

| Endpoint                               | Method | Description                |
|----------------------------------------|--------|----------------------------|
| `/ga4gh/wes/v1/service-info`           | GET    | Service info + state counts|
| `/ga4gh/wes/v1/runs`                   | POST   | Submit a workflow run      |
| `/ga4gh/wes/v1/runs`                   | GET    | List runs (paginated)      |
| `/ga4gh/wes/v1/runs/{id}`              | GET    | Full run log               |
| `/ga4gh/wes/v1/runs/{id}/status`       | GET    | Run state only             |
| `/ga4gh/wes/v1/runs/{id}/cancel`       | POST   | Cancel a run               |
| `/ga4gh/wes/v1/runs/{id}/tasks`        | GET    | List tasks for a run       |
| `/ga4gh/wes/v1/runs/{id}/tasks/{tid}`  | GET    | Get task details           |

**TES** is used internally by the workflow runners, but also exposed publicly:

| Endpoint                               | Method | Description     |
|----------------------------------------|--------|-----------------|
| `/ga4gh/tes/v1/tasks`                  | POST   | Create a task   |
| `/ga4gh/tes/v1/tasks`                  | GET    | List tasks      |
| `/ga4gh/tes/v1/tasks/{id}`             | GET    | Get task        |
| `/ga4gh/tes/v1/tasks/{id}:cancel`      | POST   | Cancel a task   |

Conquest manages the full task lifecycle — data staging, resource allocation,
state tracking, and log capture. The workflow engine submits tasks; Conquest
handles the rest.

## Adapter Architecture

Engine support is pluggable. Each engine implements a `RunAdapter` interface
and registers itself in a `Registry` at startup.

```go
type RunAdapter interface {
    Name() string
    PrepareRun(runID string, req SubmissionRequest, layout storage.Layout) (*PreparedRun, error)
}
```

When a workflow is submitted, the controller looks up the adapter by engine
name. The adapter handles everything engine-specific: resolving the workflow
source, selecting the right runner image, building CLI arguments, and
generating config files. The rest of the system — controller, K8s backends,
reconcilers, storage layout — works the same regardless of engine.

Adding a new engine means implementing this interface and registering it.

## State Machine

Runs and tasks follow the GA4GH v1.1 state model:

```
QUEUED --> INITIALIZING --> RUNNING --> COMPLETE
                                   --> EXECUTOR_ERROR
                                   --> SYSTEM_ERROR
                                   --> CANCELING --> CANCELED   (runs)
                                   --> CANCELED                (tasks)
```

Runs pass through `CANCELING` before `CANCELED` to cascade cancellation to
their tasks. Tasks go directly to `CANCELED` — the K8s Job is deleted
immediately. Transitions are enforced in code; invalid transitions return an
error.

## Testing

Tests use Go's standard `testing` package with no third-party frameworks. All
test doubles — stubs, fakes, recorders — are hand-written and live alongside
the code they test.

~180 test functions across 14 packages covering state machine transitions, K8s
job spec generation, workspace strategies, task policy enforcement, storage
layout paths, adapter logic, reconciler behavior, and more.

```bash
make test          # go test -v -race -count=1 ./...
make test-short    # go test -short -race ./...
```

## Configuration

All settings come from environment variables with the `CONQUEST_` prefix.
No config files.

| Variable                            | Default              | Description                       |
|-------------------------------------|----------------------|-----------------------------------|
| `CONQUEST_HTTP_ADDR`                | `:8080`              | Server listen address             |
| `CONQUEST_POSTGRES_DSN`             | —                    | Postgres connection string        |
| `CONQUEST_S3_BUCKET`                | `conquest-workflows` | Object storage bucket             |
| `CONQUEST_S3_ENDPOINT`              | —                    | S3 / MinIO endpoint               |
| `CONQUEST_K8S_NAMESPACE`            | `conquest`           | Kubernetes namespace for Jobs     |
| `CONQUEST_WORKSPACE_MODE`           | `emptydir`           | `emptydir`, `pvc`, or `objectfs`  |
| `CONQUEST_WORKSPACE_ACCESS_MODE`    | `RWX`                | `RWX` or `RWO` (when mode=pvc)     |
| `CONQUEST_WORKSPACE_SIZE`           | `10Gi`               | PVC / objectfs volume size        |
| `CONQUEST_OBJECTFS_CSI_SECRET_NAME` | `csi-s3-secret`      | S3 creds for the FUSE mount       |
| `CONQUEST_RECONCILER_INTERVAL`      | `5s`                 | How often reconcilers poll K8s    |
| `CONQUEST_RUNNER_IMAGE`             | —                    | Default Nextflow runner image     |
| `CONQUEST_WDL_RUNNER_IMAGE`         | —                    | Default WDL runner image          |
| `CONQUEST_SNAKEMAKE_RUNNER_IMAGE`   | —                    | Default Snakemake runner image    |

See [`internal/config/config.go`](internal/config/config.go) for the full list.

## Development

```bash
make build         # go build ./...
make test          # Run all tests
make fmt           # gofmt
make lint          # go vet
make tidy          # go mod tidy
```

## Deployment

Docker images and a Helm chart:

```bash
make docker-server          # Server image
make docker-nf-runner-all   # All Nextflow runner images (4 versions)
make docker-wdl-runner      # WDL runner image
make docker-smk-runner-all  # All Snakemake runner images (2 versions)
make docker-all             # Everything
```

The Helm chart at `deploy/charts/conquest/` includes RBAC, init-container DB
migrations, health probes, and a non-root security context.

## Design Decisions

- **WES** as the external API, **TES** as the internal execution contract
- **GA4GH v1.1** for both specs (unified state enum)
- **Postgres** as system of record
- **S3/MinIO** as the data plane for inputs, outputs, logs, and metadata
- **UUIDv7** for all IDs — time-sortable, DNS-safe, works as Postgres PKs
  and K8s labels
- Versioned storage layout (`v1/`) so the format can evolve without migration
- **Transparent** object layout in every mode — one object per file, readable
  keys, no chunking. Rules out JuiceFS-style chunk+metadata designs.
- Permissively licensed dependencies only (Apache 2.0 / MIT / BSD) so the
  stack can ship to institutional clients without copyleft review

## Tech Debt

Tracked design trade-offs for v1 are documented in
[`docs/tech-debt.md`](docs/tech-debt.md).
