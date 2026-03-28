# Conquest

Workflow execution platform for Pacific Analytics.

Implements a minimal WES-shaped control plane with an internal TES backend
on Kubernetes. Object-store-first, Nextflow first, Snakemake next.

## Architecture

```
User / Client
    |
    v
WES API (external workflow-facing contract)
    |
    v
Run Controller + Postgres (authoritative run state machine)
    |
    +--> TRS / DRS / Object Storage Resolver
    |
    +--> Engine Adapter (Nextflow first, Snakemake later)
              |
              v
          TES API (internal execution boundary)
              |
              v
     TES K8s Controller / Task Runner
              |
              v
        Kubernetes Jobs / Pods
              |
              v
  Object Storage (work, logs, outputs, artifacts)
```

## Development

```bash
# Run all tests
make test

# Build
make build

# Format
make fmt
```

## Project structure

```
conquest/
├── cmd/conquest/          # Application entry point (server, filer, runner subcommands)
├── internal/
│   ├── adapter/           # Engine-specific config generation (Nextflow)
│   ├── api/
│   │   ├── wes/           # WES v1.1 HTTP handlers
│   │   └── tes/           # TES v1.1 HTTP handlers (public + internal run-scoped)
│   ├── backend/           # Execution backends (K8s task Jobs + runner Jobs)
│   ├── config/            # Env-based application configuration
│   ├── controller/        # Business logic orchestration
│   ├── domain/            # Core domain: state machines, Run, Task, Event entities
│   ├── filer/             # Stage-in, stage-out, exec wrapper for TES tasks
│   ├── objstore/          # S3/MinIO client interface + implementation
│   ├── reconciler/        # Background state sync (task + run reconcilers)
│   ├── repository/        # Data access interfaces + Postgres implementations
│   ├── runner/            # Nextflow runner execution logic
│   ├── source/            # Workflow source validation (GitHub HTTPS)
│   └── storage/           # Object storage layout and path conventions
├── migrations/            # Postgres schema migrations (000001-000004)
├── deploy/charts/conquest/ # Helm chart
├── Dockerfile             # Conquest server image
└── Dockerfile.runner      # Nextflow runner image (Java + NF + plugins + conquest)
```

## Key decisions

- **WES** as external API, **TES** as internal execution contract
- **GA4GH v1.1** for both specs (unified state enum)
- **Postgres** as system of record
- **Object storage** (S3/MinIO) as the data plane
- **UUIDv7** for run and task IDs (sortable, K8s-safe)
- Versioned storage layout (`v1/`) for future evolution
