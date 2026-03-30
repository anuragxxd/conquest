# Conquest

Workflow execution service. Runs Nextflow and Snakemake
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
                      |  (Nextflow / SMK)  |     | (K8s > DB) |
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
3. The runner executes the workflow engine (Nextflow or Snakemake), which
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
| Snakemake | 9.17.3   | Yes     | TES dispatch via `conquest-tes` executor plugin      |
| Snakemake | 7.32.4   |         | Local execution in runner pod                        |

[nf-ga4gh]: https://github.com/nextflow-io/nf-ga4gh

Nextflow versions use the upstream [`nf-ga4gh`][nf-ga4gh] plugin to submit
tasks to Conquest's TES API.

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

Both engines dispatch work through TES, so users control CPU, memory, and disk
for each task pod directly in their workflow. Nextflow uses process directives
(`cpus`, `memory`, `disk`); Snakemake uses rule resources (`cpus`, `mem_gb`,
`disk_gb`). The engine plugin translates these to TES resource fields, and
Conquest maps them to Kubernetes resource requests/limits.

A task policy layer fills in defaults when omitted (1 CPU, 2 GB RAM, 8 GB
disk) and enforces platform-level floors and ceilings. See
`CONQUEST_TASK_DEFAULT_*`, `CONQUEST_TASK_MIN_*`, and `CONQUEST_TASK_MAX_*`
in [`internal/config/config.go`](internal/config/config.go).

## Workspace Modes

Controls how the runner pod and task pods share (or don't share) a filesystem.

| Mode          | Volume Type          | Shared Filesystem | Notes                                                                                                     |
|---------------|----------------------|-------------------|-----------------------------------------------------------------------------------------------------------|
| `pvc` + `RWX` | PVC (ReadWriteMany)  | Yes               | **Preferred.** Requires NFS or a similar StorageClass.                                                    |
| `pvc` + `RWO` | PVC (ReadWriteOnce)  | Yes               | Good fallback when NFS isn't available. Co-locates tasks on the same node as the runner via pod affinity. |
| `emptydir`    | Kubernetes emptyDir  | No                | Each task stages inputs from S3 and uploads outputs back to S3 independently. Fully comapatible with Nextflow and simple Snakemake runs. Good fallback when the cluster has no persistent storage provisioner. |

```bash
CONQUEST_WORKSPACE_MODE=pvc          # "emptydir" or "pvc"
CONQUEST_WORKSPACE_ACCESS_MODE=RWX   # "RWX" or "RWO"
CONQUEST_WORKSPACE_SIZE=10Gi
```

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
| `CONQUEST_WORKSPACE_MODE`           | `emptydir`           | `emptydir` or `pvc`              |
| `CONQUEST_WORKSPACE_ACCESS_MODE`    | `RWX`                | `RWX` or `RWO` (when mode=pvc)   |
| `CONQUEST_WORKSPACE_SIZE`           | `10Gi`               | PVC size                          |
| `CONQUEST_RECONCILER_INTERVAL`      | `5s`                 | How often reconcilers poll K8s    |
| `CONQUEST_RUNNER_IMAGE`             | —                    | Default Nextflow runner image     |
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

## Tech Debt

Tracked design trade-offs for v1 are documented in
[`docs/tech-debt.md`](docs/tech-debt.md).
