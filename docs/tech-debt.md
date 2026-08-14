# Tech Debt

Tracked decisions where we deliberately chose a simpler v1 path over a
more complete implementation. Each item includes the rationale, the risk
if left indefinitely, and the trigger condition for when it should be
addressed.

---

## TD-001: One K8s Job per TES task instead of taskmaster pattern

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
Each TES task maps to exactly one Kubernetes Job. No coordinating
taskmaster pod, no staged input/output filer jobs.

**Why:**
- Fastest path to a working Nextflow-first vertical slice.
- `nf-ga4gh` only creates single-executor TES tasks, so multi-executor
  orchestration is not needed for the first engine.
- Fewer K8s objects to manage, simpler reconciliation, easier debugging.

**What we gave up:**
- No multi-executor TES task support (spec allows sequential executors
  with shared volumes, `ignore_error`, different images per step).
- Input/output staging is handled inline rather than as separate filer
  stages — harder to isolate staging failures from execution failures.
- Weaker alignment with TESK's taskmaster model and Funnel's
  worker-owned staging pattern.

**Risk if never addressed:**
- Cannot support TES clients that submit multi-executor tasks.
- Snakemake's future needs may require richer staging (Phase 3).
- Harder to implement per-executor log capture cleanly.

**When to address:**
- Before adding Snakemake support (Phase 3).
- If any TES client needs multi-executor tasks.
- If input/output staging failures become hard to diagnose.

---

## TD-002: PAUSED and PREEMPTED states defined but not wired

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
Both states exist in the `State` enum for GA4GH spec compliance, but
the transition maps have empty target sets — nothing can transition
into or out of them.

**Why:**
- No v1 use case for pausing runs.
- PREEMPTED requires spot/preemptible instance awareness in the K8s
  backend, which is out of scope for v1.
- `nf-ga4gh` does not handle either state (a task in PREEMPTED would
  hang forever in its polling loop).

**Risk if never addressed:**
- Non-compliant with the full GA4GH TES/WES state machine.
- Cannot support spot instance preemption gracefully.
- Clients that check for PAUSED/PREEMPTED will get unexpected behavior.

**When to address:**
- When adding spot/preemptible node pool support.
- When a client or compliance test requires these states.

---

## TD-003: Task CANCELING state not emitted (nf-ga4gh compatibility)

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
TES tasks transition directly to CANCELED, skipping the CANCELING
intermediate state. Only WES runs use CANCELING.

**Why:**
- `nf-ga4gh`'s `STARTED_STATUSES` and `COMPLETE_STATUSES` do not
  include CANCELING. A task in CANCELING state would hang forever
  in the plugin's polling loop.

**Risk if never addressed:**
- If the nf-ga4gh plugin is fixed upstream to handle CANCELING, we
  should revisit.
- Other TES clients may expect CANCELING as a valid intermediate state.

**When to address:**
- When nf-ga4gh adds CANCELING support.
- When adding a second TES client that expects it.

---

## TD-004: WES multipart submission — minimal first implementation

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
Implemented basic multipart/form-data parsing for `POST /runs` plus
JSON body as a convenience alternative. `workflow_attachment` file
upload is not yet supported. First slice accepts workflow URLs pointing
to object storage, not uploaded bundles.

**Why:**
- Multipart with JSON-encoded string fields and binary attachments is
  complex server-side parsing.
- The first slice uses direct object-storage workflow refs, so
  attachments are not needed yet.

**Risk if never addressed:**
- Cannot support clients that upload workflow files directly via the
  WES API.
- Non-compliant with the full WES submission flow.

**When to address:**
- When external clients need to submit workflows by upload.
- Before any public API exposure.

---

## TD-005: No TRS resolution in first slice

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
First resolver implementation handles direct object-storage URIs only.
TRS (Tool Registry Service) resolution is deferred.

**Why:**
- Fastest path to end-to-end execution.
- `trs-cache-filer` exists as a strong reference for when we add it.

**Risk if never addressed:**
- Cannot resolve workflows from a GA4GH TRS endpoint.
- Limits interoperability with the broader GA4GH ecosystem.

**When to address:**
- After the first successful end-to-end workflow execution.
- Before integrating with any external workflow registry.

---

## TD-006: No user-scoped credentials in v1

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
Platform-managed credentials only. Object storage and image pull
credentials are provided via Kubernetes Secrets or service accounts
configured by the platform operator, not passed through the WES API.

**Why:**
- Simplest secure model for the first slice.
- The WES spec explicitly says credential passing is out of scope.
- Avoids premature credential plumbing through the entire stack.

**Risk if never addressed:**
- Cannot support multi-tenant workloads where different users access
  different storage buckets or registries.
- Cannot support cross-account data access patterns.

**When to address:**
- When multi-tenancy is required.
- When users need to bring their own storage credentials.

---

## TD-007: Missing WES ServiceInfo required fields

**Decided:** 2026-03-25
**Status:** Active (partially addressed)

**What we chose:**
`GET /service-info` returns basic fields plus `system_state_counts`.
The full WES spec also requires `auth_instructions_url` and richer
`default_workflow_engine_parameters`.

**Progress:**
- `auth_instructions_url` field exists in the `ServiceInfo` struct but
  is always empty string — never populated.
- `default_workflow_engine_parameters` is still completely absent from
  both the struct and the handler.

**Why:**
- Auth instructions and default params need design decisions.
- Not blocking for the first vertical slice.

**When to address:**
- Before exposing the API to external clients.
- When implementing auth (Phase 2).

---

## TD-008: TES `backend_parameters` not supported

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
The TES spec's `backend_parameters` and `backend_parameters_strict`
extension mechanism is not implemented. No way for clients to request
K8s-specific features (GPU, storage class, node pools) through the
TES API.

**Why:**
- Requires design decisions about which K8s-specific parameters to
  expose and advertise.
- Not needed for the first Nextflow vertical slice.

**Risk if never addressed:**
- Cannot support GPU workloads or specialized node pools through TES.
- Non-compliant with TES v1.1 extension mechanism.

**When to address:**
- When GPU or specialized hardware support is needed.
- When exposing the TES API to external clients beyond Nextflow.

---

## TD-009: Postgres repository uses raw pgx, not sqlc

**Decided:** 2026-03-25
**Status:** Active — approaching trigger threshold (~29 queries)

**What we chose:**
Repository implementations use hand-written pgx queries instead of
sqlc-generated code. The sqlc.yaml config is present for future use.

**Why:**
- sqlc codegen requires a running Postgres instance at generation time.
- Hand-written queries are faster to iterate on during early development.
- The query surface is small enough to manage by hand.

**Current state:**
The repository layer now has ~29 distinct SQL operations across
`runs.go`, `tasks.go`, and `events.go`. This is at the upper end of
the original trigger threshold (~20-30).

**When to address:**
- When the query count exceeds ~20-30 and type safety becomes a concern.
- When onboarding new developers who benefit from generated code.

---

## TD-010: Incomplete pod security context on task Jobs

**Decided:** 2026-03-26
**Status:** Active (partially addressed)

**What we chose:**
Task Jobs have minimal `securityContext`. The executor runs as whatever
user the container image specifies.

**Progress:**
- Pod-level `fsGroup: 1000` is now set for PVC-backed workspaces via
  `podSecurityContextForWorkspace()` in `podspec.go`.
- The server Deployment has full hardening (`runAsNonRoot`, `capabilities.drop: ALL`,
  `readOnlyRootFilesystem`, `seccompProfile: RuntimeDefault`).

**What remains:**
- emptyDir mode (the default) still gets `nil` SecurityContext.
- No container-level SecurityContext on any executor, init, or runner container
  (`runAsNonRoot`, `allowPrivilegeEscalation`, `capabilities`, `seccompProfile`
  are all unset).
- The PVC-mode pod-level context only sets `fsGroup`, not `runAsNonRoot`,
  `runAsUser`, or `seccompProfile`.

**Why:**
- Many bioinformatics tools expect to run as root or have specific
  UID requirements.
- Locking down the security context requires understanding each
  workflow's tool requirements.

**Risk if never addressed:**
- Task pods run with broader privileges than necessary.
- Does not match the platform's non-root UID 1000 convention.

**When to address:**
- During Phase 2 hardening.
- When deploying to a production cluster with PodSecurityStandards.

---

## TD-014: Workflow source resolution and engine-parameter normalization are hardcoded to public GitHub HTTPS in Batch D

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
Batch D accepts only public GitHub HTTPS repository URLs in `workflow_url`,
and parses a narrow Nextflow-specific subset of `workflow_engine_parameters`
(`revision`, `entry`, `main_script`) directly in Conquest. It does not yet
have a pluggable adapter/resolver layer for other workflow source protocols
or parameter schemas.

**Why:**
- Smallest path to the first end-to-end Nextflow execution.
- Lets Nextflow resolve the public Git repository itself inside the runner Job.
- Avoids building broader workflow source resolution in the same batch.

**What we gave up:**
- No `s3://`, SSH Git (`git@github.com:...`), private Git, TRS, DOS/DRS,
  or WES attachment support in Batch D.
- Workflow source handling and workflow parameter normalization are partially
  hardcoded instead of hidden behind a reusable adapter interface.
- Only full 40-char commit SHAs are accepted as revisions; tags and branches
  are rejected for reproducibility.

**Risk if never addressed:**
- Source handling will become scattered and engine-specific.
- Future support for Git variants, object storage, DOS/DRS, and other
  engines will be harder to add cleanly.
- WES request compatibility will drift as more source-specific exceptions
  are added.

**When to address:**
- Before adding non-public Git sources (SSH, private repos).
- Before adding S3/TRS/DOS/DRS workflow sources.
- Before Snakemake support.
- When introducing a broader workflow-source adapter/plugin system.

---

## TD-015: Runner plugin versions are pinned in Go source, not configurable at deploy time

**Decided:** 2026-03-26
**Status:** Active (partially addressed)

**What we chose:**
Plugin version-to-runtime mappings are hardcoded in Go source. Adding
a new plugin version requires a code change and rebuild.

**Progress:**
- Multiple Nextflow versions now supported (24.10.4, 25.04.3, 25.10.2,
  25.10.4) with per-version runner images.
- The nf-runner Dockerfile uses `ARG NXF_BASE_IMAGE` build arg.
- Runtime image selection is configurable via
  `CONQUEST_NEXTFLOW_RUNNER_IMAGES` env var and Helm values.

**What remains:**
- Plugin versions (nf-ga4gh, nf-amazon) are still hardcoded in
  `internal/engine/nextflow/runtime.go` — not configurable via
  environment variables or Helm values.
- Each new Nextflow version still requires a separate Docker image build.

**Why:**
- Reproducibility and simplicity for the first end-to-end slice.
- No need for runtime plugin resolution with network access.

**Risk if never addressed:**
- Plugin version updates require code changes and redeployment.
- Cannot support workflows that require specific plugin versions
  without modifying Go source.

**When to address:**
- When plugin version flexibility is needed across deployments.
- When adding support for user-specified plugin versions.

---

## TD-016: nf-ga4gh TES executor assumes `bash` exists in the task image

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
Batch D relies on nf-ga4gh's generated TES executor command as-is. That
command invokes `bash -o pipefail -c 'bash /work/.command.run ...'`, so
workflow task images must include a working `bash` binary.

**Why:**
- The plugin hardcodes `bash` in `TesTaskHandler` and Conquest executes the
  TES executor command faithfully.
- Supplying a portable Bash binary inside arbitrary user images is non-trivial
  because of dynamic linking, filesystem layout, and image compatibility.
- The fastest path to end-to-end execution is to require acceptance workflows
  to use Bash-capable images.

**Risk if never addressed:**
- Public Nextflow workflows using minimal images like `alpine` will fail even
  when the user command itself is simple.
- This behavior is surprising unless documented clearly.

**When to address:**
- Before broadening support beyond controlled acceptance workflows.
- When evaluating alternatives to nf-ga4gh or adding image/toolchain
  compatibility shims.

---

## TD-017: objectfs inherits S3-backed FUSE POSIX limitations

**Decided:** 2026-08-12
**Status:** Accepted — properties of the approach, not defects

**What we chose:**
`objectfs` mounts an S3 prefix per run via GeeseFS. The layout is transparent
(one object per file, readable keys) which rules out chunk+metadata designs
like JuiceFS that would give better POSIX fidelity.

**What we gave up:**
- No hard links (`ENOTSUP`). Currently harmless: `internal/runner/wdl.go`
  sets `output_hardlinks = false`, so miniwdl uses symlinks.
- No "invisible" deleted files — unlinking an open file yields `ESTALE`, so
  `tempfile.NamedTemporaryFile(delete=True)` patterns fail.
- Advisory locks are kernel-local per mount: they serialise within a pod but
  not across pods.
- Concurrent writes to one object from multiple pods are unsafe.

**Validated working** (against MinIO, with `--enable-perms --enable-mtime
--enable-specials`): rename incl. directories, truncate, partial/seek writes,
tmp+fsync+rename, chmod, utime, symlinks, `flock`, `fcntl` `F_SETLK`, `lockf`,
`mkfifo`, `mknod`, sqlite, in-place `git` operations. ~1100 file creates/s,
131 MB/s write, 137 MB/s read, 2000-file cold walk in 0.29s.

**When to address:**
- If a workflow needs hard links or cross-pod advisory locking, use
  `pvc` + `RWX` for that workload instead.

---

## TD-018: Conquest task pods can evict platform components

**Decided:** 2026-08-12
**Status:** Active — highest-severity open item

**What happens:**
Task pods take their ephemeral-storage request from the TES `disk_gb` field.
A Snakemake run with `jobs: 8` and 9Gi per task reserves 72Gi on a 94Gi node.
Platform components (`pgcluster`, `democratic-csi`, ClickHouse, Keycloak's
Postgres) declare **no** ephemeral-storage request and **no** priorityClass,
and the kubelet evicts pods exceeding their request first — so a request of
zero makes them the *first* candidates.

Observed on the preview cluster: a single `rna-seq-star-deseq2` run drove
`DiskPressure` and evicted the Postgres primary, the ClickHouse instance, the
ClickHouse operator, Keycloak's Postgres and — worst — the
`democratic-csi-local-node` DaemonSet. With the CSI driver gone, *no* pod on
the node could mount any PVC, so Postgres could not restart. Recovery needed
a manual DaemonSet restart.

**Risk if never addressed:**
A workflow can take down the platform it runs on. On a single-node or small
cluster this is a full outage, not degradation.

**When to address:**
Before any client-facing deployment. Candidate fixes:
- `system-cluster-critical` priorityClass + non-zero ephemeral requests on
  platform components (owned by the infra chart, not Conquest)
- cap concurrent task pods per node
- right-size `CONQUEST_RUNNER_EPHEMERAL_STORAGE_REQUEST` (currently 20Gi
  per runner, which is large for a process whose work tree lives in S3)
- back Snakemake conda environments with a volume instead of node ephemeral

---

## TD-019: conquest binary is compiled into all eight images

**Decided:** 2026-08-12
**Status:** Active

**What we chose:**
The server image and all seven runner images each build the `conquest` binary
from source. Task executors get it from `CONQUEST_IMAGE`; runners get it from
their own image.

**Why it matters:**
A change to shared code (`internal/filer`, `internal/runner`,
`internal/sharedfs`) is only live where the image has been rebuilt, with no
signal when they disagree. This bit during the umask fix: the server image
carried it, the runner images still had a four-month-old binary, so the fix
was silently half-applied and appeared not to work.

**When to address:**
Build the binary once per commit and `COPY --from` a single published
artifact, so shared-code changes cannot ship inconsistently.

---

## TD-020: terminal-state Job deletion destroys pod logs

**Decided:** 2026-08-12
**Status:** Active — regression introduced by the PV/PVC leak fix

**What we chose:**
`cleanupWorkspace` deletes the run's Jobs before releasing the PVC, because
`pvc-protection` otherwise pins the volume for the full Job TTL.

**What we gave up:**
Pod logs for the runner and tasks disappear the moment a run goes terminal.
For a *failed* run that is exactly when they are needed. `logs/run.log` is
uploaded to S3, but not when the runner crashes before the upload — leaving
no log at all. Encountered twice while diagnosing Snakemake failures.

**When to address:**
Soon. Options: retain Jobs on non-success terminal states and accept the
volume being pinned for the TTL; make the `run.log` upload crash-safe; or
stream runner logs out continuously rather than at exit.

---

## TD-021: two tech-debt files

**Status:** Active — housekeeping

`TECHDEBT.md` (repo root) and `docs/tech-debt.md` have diverged. The root
copy still claims S3 FUSE mounts were rejected for POSIX-compatibility
reasons (TD-011), which `objectfs` has superseded. Pick one location and
delete the other.
