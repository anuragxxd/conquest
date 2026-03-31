# WDL Integration Plan

## Goal

Add WDL support to Conquest using a TES-native execution path, with Cromwell as the first WDL engine.

Target architecture:

`WES -> WDL adapter -> Cromwell runner job -> Conquest internal TES -> K8s Jobs`

This plan assumes GitHub support is required in v1.

## Executive Summary

The right first implementation is:

- `workflow_type = WDL`
- `workflow_engine = cromwell`
- WDL source comes from GitHub
- Cromwell runs inside the Conquest runner job
- Cromwell dispatches WDL calls to Conquest through the internal run-scoped TES endpoint
- Conquest TES launches one K8s Job per TES task
- WDL runs require shared PVC workspace mode

There is one important compatibility constraint:

Cromwell's TES backend is shared-filesystem-first, while Conquest's current TES execution path is object-storage-first.

So the WDL integration is not only a new adapter and runner. It also requires a small TES compatibility layer so Conquest can accept Cromwell's local/shared-path TES payloads.

## Core Decisions

### 1. Execution Model

Conquest remains the control plane.

Conquest owns:

- WES submission and run lifecycle
- runner Job creation
- internal TES API
- TES task persistence
- K8s Job creation for TES tasks
- run and task reconciliation
- shared workspace provisioning

Cromwell owns:

- WDL parsing and validation
- WDL graph orchestration
- scatter, conditional, and subworkflow semantics
- retry semantics inside the workflow engine
- TES task submission for each WDL call

### 2. Source Model

GitHub is a first-class v1 source mode.

The canonical WDL GitHub submission contract should be:

- `workflow_url = https://github.com/<org>/<repo>`
- `workflow_engine_parameters.revision = <40-char commit SHA>`
- `workflow_engine_parameters.main_workflow_path = <repo-relative path to the primary .wdl>`

This is required because WDL execution needs both:

- a stable import root
- an explicit primary WDL document

A repo URL alone is not enough for WDL.

### 3. Workspace Model

WDL requires shared PVC workspace mode.

Conquest must reject WDL runs when workspace mode is `emptydir`.

Required path convention:

- runner mounts workspace at `/workspace`
- Cromwell `root = "/workspace/cromwell-executions"`
- Cromwell `dockerRoot = "/workspace/cromwell-executions"`
- TES task pods mount the same shared PVC at `/workspace`

This ensures the paths Cromwell generates are visible to both the runner and the TES task pods.

### 4. WDL Version Scope

Initial supported scope:

- WDL `1.0`
- WDL `1.1`

Deferred:

- WDL `1.2`
- WDL `1.3`
- broad `requirements` / `hints` support
- call caching
- full conformance claims

### 5. Runtime Attribute Scope

Initial supported runtime attribute subset:

- `docker` / `container`
- `dockerWorkingDir`
- `cpu`
- `memory`
- `disk`
- `disks` for simple single local-disk cases only
- `preemptible`

We should not advertise broader compatibility than this.

### 6. TES Backend Policy

Cromwell should submit to Conquest using the internal run-scoped TES endpoint:

`/internal/runs/{run_id}/ga4gh/tes/v1/tasks`

This avoids depending on user-supplied TES tags to attach tasks to a run.

## Why GitHub Support Works For WDL

WDL imports are resolved relative to the location of the current document when no scheme is present. That makes GitHub support viable if and only if Conquest treats the checked-out repo as the import root and requires the caller to provide the primary WDL path.

The GitHub source contract therefore must include:

- immutable repo revision
- explicit primary WDL path
- checkout of the full repo at that exact revision
- relative import resolution from the checked-out filesystem

This is a better fit than trying to infer the main WDL automatically.

## High-Level Architecture

### Submission Path

1. User submits a WDL run through WES.
2. Conquest routes the request to a WDL adapter.
3. The adapter validates the GitHub source, WDL version, and engine parameters.
4. Conquest launches a WDL runner K8s Job.
5. The runner clones the GitHub repo at the exact commit SHA.
6. The runner validates the primary WDL path and writes `inputs.json`.
7. The runner generates Cromwell TES backend config.
8. The runner invokes Cromwell.
9. Cromwell submits each WDL call to Conquest internal TES.
10. Conquest TES persists each task and launches one K8s Job per TES task.
11. K8s task Jobs execute using the shared PVC workspace.
12. Cromwell completes the workflow and exits.
13. Conquest reconciles the runner job and marks the WES run terminal.

### Key Compatibility Requirement

Conquest internal TES must accept Cromwell TES tasks that reference shared local filesystem paths.

Current Conquest TES assumes object-storage-oriented task inputs and outputs. Cromwell TES assumes shared workspace paths are already available.

That mismatch must be closed for the integration to work.

## Proposed User-Facing WES Contract

### Required Fields

- `workflow_type = WDL`
- `workflow_type_version = 1.0` or `1.1`
- `workflow_engine = cromwell`
- `workflow_url = https://github.com/<org>/<repo>`

### Required Engine Parameters

- `revision`
- `main_workflow_path`

### Recommended Example

```json
{
  "workflow_type": "WDL",
  "workflow_type_version": "1.1",
  "workflow_engine": "cromwell",
  "workflow_url": "https://github.com/acme-org/my-wdl-pipelines",
  "workflow_engine_parameters": {
    "revision": "0123456789abcdef0123456789abcdef01234567",
    "main_workflow_path": "workflows/rnaseq/main.wdl"
  },
  "workflow_params": {
    "MyWorkflow.input_bam": "s3://example-bucket/data/sample.bam"
  }
}
```

## Supported and Unsupported Source Cases

### Supported In V1

- public GitHub repo URLs
- full commit SHA checkout
- repo-relative primary WDL path
- repo-relative imports within the checked-out repo
- submodules if present in the pinned checkout and successfully updated recursively

### Rejected In V1

- repo URL without `main_workflow_path`
- branch names
- tags
- inferred primary WDL discovery
- non-GitHub git hosts
- WDL submission on `emptydir`
- source forms that cannot guarantee stable import resolution

### Open Decision

Two policy questions remain:

- whether private GitHub should be supported in v1
- whether arbitrary external `https://` imports should be allowed in v1

Default recommendation:

- public GitHub only in v1
- repo-contained imports only in v1

## Conquest Changes Required

### A. WES Layer

Files likely affected:

- `internal/api/wes/types.go`
- `internal/api/wes/handlers.go`
- `internal/controller/run_controller.go`
- `internal/adapter/registry.go`

Required work:

- add `WDL` routing support
- map `WDL` submissions to the `cromwell` engine
- validate `workflow_type_version`
- expose WDL in WES service-info
- expose the supported Cromwell version in WES service-info

Important note:

WES multipart `workflow_attachment` support is not required for GitHub-first WDL v1 if GitHub is the canonical source mode.

### B. Config Layer

Files likely affected:

- `internal/config/config.go`
- `internal/config/config_test.go`
- deployment manifests and chart values
- `Makefile`

Required work:

- add Cromwell runner image config
- add version-to-image mapping for Cromwell if needed
- add WDL-specific runtime configuration
- add validation that WDL requires PVC workspace mode when enabled

Suggested config surface:

- `CONQUEST_CROMWELL_RUNNER_IMAGE`
- `CONQUEST_CROMWELL_RUNNER_IMAGES`
- `CONQUEST_WDL_DEFAULT_VERSION`
- `CONQUEST_WDL_ALLOW_EXTERNAL_IMPORTS`
- `CONQUEST_WDL_GITHUB_PRIVATE_ENABLED`

The final names can be refined during implementation.

### C. WDL Runtime Policy

New file:

- `internal/engine/wdl/runtime.go`

Responsibilities:

- define supported Cromwell version(s)
- define supported WDL versions
- resolve default runtime version
- expose runtime compatibility rules to adapter and service-info

### D. WDL Adapter

New file:

- `internal/adapter/wdl_run.go`

Responsibilities:

- validate source contract
- validate `revision`
- validate `main_workflow_path`
- normalize the stored request payload
- choose the runner image
- build runner command and env vars

Recommended engine parameter schema:

```json
{
  "revision": "<40-char sha>",
  "main_workflow_path": "path/to/workflow.wdl"
}
```

Validation rules:

- `revision` must be a full 40-char SHA
- `main_workflow_path` must be non-empty
- `main_workflow_path` must be repo-relative
- `main_workflow_path` must not be absolute
- `main_workflow_path` must not traverse outside repo root

The adapter should remain thin and should not implement WDL semantics.

### E. Runner CLI and Runner Implementation

New files:

- `cmd/conquest/runner_wdl.go`
- `internal/runner/wdl.go`

Responsibilities:

- clone repo to `/workspace/workflow`
- checkout exact SHA
- run `git submodule update --init --recursive`
- validate `main_workflow_path`
- write `inputs.json`
- generate Cromwell config
- invoke Cromwell
- upload `run.log`
- write final outputs summary artifact
- exit with a status that correctly reflects workflow success or failure

Important implementation rule:

Do not assume Cromwell process exit behavior is sufficient until verified. The runner should explicitly validate terminal workflow outcome and surface failure clearly.

### F. Runner Image

New directory:

- `docker/wdl-runner/`

Contents:

- JRE
- pinned Cromwell jar
- optional `womtool`
- `conquest` binary

Image requirements:

- explicit version pinning
- checksum verification for downloaded artifacts
- non-root runtime
- workdir `/workspace`

### G. Storage Layout

File likely affected:

- `internal/storage/layout.go`

Likely additions:

- helper for WDL work artifacts
- helper for final WDL outputs summary artifact
- helper for Cromwell metadata if we choose to persist it

At minimum we should have a stable place for:

- run log
- final workflow outputs JSON
- optional Cromwell metadata artifact

### H. Internal TES Compatibility Layer

Files likely affected:

- `internal/api/tes/handlers.go`
- `internal/backend/k8s/jobspec.go`
- `internal/filer/stagein.go`
- `internal/filer/stageout.go`
- `internal/filer/exec.go`

This is the most important Conquest-specific change after the runner itself.

#### Problem

Current Conquest TES validation and staging assume object-storage-oriented task inputs and outputs.

Cromwell TES sends local/shared-path-oriented tasks, including:

- generated script input
- local execution directory outputs
- stdout/stderr/rc under the shared workspace

#### Required Behavior

For internal WDL/Cromwell TES tasks only:

- allow trusted absolute local input URLs/paths under the shared workspace
- allow trusted absolute local output URLs/paths under the shared workspace
- make stage-in a no-op when the input already exists on the shared PVC
- make stage-out a no-op when the output is already on the shared PVC

Important scope rule:

This compatibility layer should be limited to internal run-scoped Cromwell submissions. Public TES should remain strict.

### I. Workspace Scope Enforcement

Files likely affected:

- `internal/api/tes/handlers.go`
- `internal/backend/k8s/jobspec.go`
- `internal/backend/k8s/workspace.go`

Current behavior uses TES tags to choose shared vs isolated workspace scope.

For WDL, Conquest should force shared workspace scope for internal Cromwell tasks.

Recommended implementation:

- when creating an internal TES task for a WDL/Cromwell run, inject `conquest_workspace_scope=shared` if not already present

This is smaller and safer than trying to infer shared scope inside the K8s backend without run context.

### J. WES Outputs Surface

Files likely affected:

- `internal/api/wes/handlers.go`
- storage layout helpers
- runner implementation
- possibly DB model if we decide to persist outputs directly

Current Conquest WES response has an `Outputs` field, but the platform does not appear to persist final workflow outputs yet.

For WDL we should implement this deliberately.

Recommended v1 behavior:

- runner writes final workflow outputs JSON to a stable artifact path
- WES `GET /runs/{id}` returns those outputs

This should be treated as part of the first complete delivery, not a later enhancement.

## Cromwell Configuration For Conquest

The runner should generate TES backend config along these lines:

```hocon
backend {
  default = "TES"
  providers {
    TES {
      actor-factory = "cromwell.backend.impl.tes.TesBackendLifecycleActorFactory"
      config {
        endpoint = "http://conquest.services.svc/internal/runs/<run_id>/ga4gh/tes/v1/tasks"
        root = "/workspace/cromwell-executions"
        dockerRoot = "/workspace/cromwell-executions"
        output-mode = "root"
        glob-link-command = "ls -L GLOB_PATTERN 2> /dev/null | xargs -I ? ln -s ? GLOB_DIRECTORY"
        use_tes_11_preview_backend_parameters = false
        poll-backoff {
          min = "15 seconds"
          max = "60 seconds"
          multiplier = 1.2
          randomization-factor = 0.1
        }
      }
    }
  }
}
```

Notes:

- `root` and `dockerRoot` should point at the same shared mount path in Conquest
- `output-mode = "root"` reduces fragile per-output declaration handling
- symlink-safe glob handling is recommended for Kubernetes-backed storage
- conservative polling is preferred initially because Conquest does not currently emit the optional TES cost metadata Cromwell uses to reduce FULL polling

## Risk Register

### 1. TES Path Model Mismatch

Risk:
Conquest TES currently assumes S3/http inputs, while Cromwell TES assumes shared local paths.

Mitigation:
Implement a narrow internal TES compatibility layer for local shared-path tasks.

### 2. Source Ambiguity

Risk:
Repo URL without explicit primary WDL path will lead to brittle or incorrect workflow selection.

Mitigation:
Require `main_workflow_path`.

### 3. Run Success Semantics

Risk:
Runner Job success may not always reflect workflow success unless Cromwell completion semantics are verified carefully.

Mitigation:
Runner should explicitly validate terminal workflow outcome and exit accordingly.

### 4. Outputs Not Surfaced

Risk:
The workflow may execute correctly but WES responses remain incomplete.

Mitigation:
Implement final outputs artifact generation and return it in `GET /runs/{id}`.

### 5. Workspace Mode Misconfiguration

Risk:
WDL runs may be submitted while Conquest is in `emptydir` mode.

Mitigation:
Reject WDL runs when workspace mode is not PVC-backed shared storage.

### 6. External Imports

Risk:
Allowing arbitrary `https://` imports introduces reproducibility and network availability problems.

Mitigation:
Default to repo-contained imports only in v1.

## Test Plan

### Unit Tests

Add coverage for:

- WDL engine inference
- WDL adapter validation
- GitHub revision validation
- `main_workflow_path` validation
- WES service-info for WDL
- Cromwell config generation
- internal TES local-path validation rules
- stage-in no-op behavior for shared local inputs
- stage-out no-op behavior for shared local outputs
- shared workspace scope injection

### Integration Tests

End-to-end tests should cover:

- single-task WDL from GitHub
- relative import within repo
- scatter
- subworkflow
- conditional execution
- executor failure
- image pull failure
- cancellation during execution
- file outputs
- glob outputs
- directory outputs
- stdout/stderr/rc behavior

### Operational Tests

- RWX PVC shared workspace
- larger scatter fan-out
- reconciler behavior with many TES tasks
- runner cancellation
- workspace cleanup on terminal run state

## Phased Implementation Plan

### Phase 0: Finalize The Contract

- confirm GitHub source contract
- confirm public vs private GitHub policy
- confirm external import policy
- confirm WDL requires PVC workspace
- confirm supported WDL versions and runtime attribute subset

Deliverable:

- approved design document

### Phase 1: Engine Wiring

- add `WDL -> cromwell` engine inference
- add WDL to adapter registry
- add WDL to WES service-info
- add WDL runtime policy
- add config support for runner image/version

Deliverable:

- control plane recognizes WDL as a supported engine

### Phase 2: WDL Adapter

- implement `internal/adapter/wdl_run.go`
- validate GitHub repo URL, SHA, main path
- normalize stored request payload
- build runner spec

Deliverable:

- WDL submission reaches runner launch with correct normalized inputs

### Phase 3: Cromwell Runner

- add CLI entrypoint
- add runner implementation
- build runner image
- implement git checkout and main-path validation
- generate inputs and config
- invoke Cromwell
- upload run log

Deliverable:

- runner can start Cromwell for a checked-out GitHub WDL repo

### Phase 4: TES Compatibility Layer

- add internal local-path TES support
- add shared workspace no-op staging behavior
- force shared workspace scope for WDL tasks

Deliverable:

- Cromwell-submitted TES tasks can execute correctly on Conquest

### Phase 5: Outputs and Run Surface

- write final outputs artifact
- return workflow outputs in WES run log response
- improve error summaries and failure visibility

Deliverable:

- WES run responses are complete enough for WDL users

### Phase 6: Hardening

- broaden test corpus
- verify larger scatters
- confirm cancellation behavior
- improve operational logging and observability
- document unsupported features clearly

Deliverable:

- production-ready first WDL release

## Acceptance Criteria

The first WDL delivery is complete when all of the following are true:

- Conquest WES accepts `workflow_type = WDL`
- WDL runs can be submitted from GitHub using repo URL + SHA + primary WDL path
- the runner checks out the exact repository state
- Cromwell starts successfully inside the runner pod
- Cromwell submits tasks to Conquest internal TES
- Conquest TES launches K8s Jobs for those tasks
- shared workspace paths align between runner and task pods
- repo-relative imports work
- simple workflows complete successfully
- scatter and subworkflow execution work
- cancellation works
- workflow outputs are returned through WES
- WDL runs are rejected when workspace mode is incompatible
