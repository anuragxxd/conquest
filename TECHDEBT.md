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
**Status:** Active

**What we chose:**
`GET /service-info` returns basic fields plus `system_state_counts`.
The full WES spec also requires `auth_instructions_url` and richer
`default_workflow_engine_parameters`.

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

## TD-009: TES ListTasks returns empty (stub)

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
`GET /ga4gh/tes/v1/tasks` returns an empty list. We don't have a
general-purpose "list all tasks across all runs" query yet. Tasks are
accessible per-run via the WES task endpoints.

**Why:**
- The primary consumer (nf-ga4gh plugin) does not call ListTasks.
- Adding a general task listing with all TES filters (name_prefix,
  state, tag_key/tag_value) is non-trivial.

**When to address:**
- When a TES client needs the list endpoint.
- When building admin/observability tooling.

---

## TD-010: Postgres repository uses raw pgx, not sqlc

**Decided:** 2026-03-25
**Status:** Active

**What we chose:**
Repository implementations use hand-written pgx queries instead of
sqlc-generated code. The sqlc.yaml config is present for future use.

**Why:**
- sqlc codegen requires a running Postgres instance at generation time.
- Hand-written queries are faster to iterate on during early development.
- The query surface is small enough to manage by hand.

**When to address:**
- When the query count exceeds ~20-30 and type safety becomes a concern.
- When onboarding new developers who benefit from generated code.

---

## TD-011: emptyDir workspace (no persistent or S3-mounted workdir)

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
Task executor containers use a Kubernetes `emptyDir` volume for the
workdir (default `/work`). Inputs are downloaded from S3 by an init
container, and outputs are uploaded by the reconciler after completion.

**Why:**
- `emptyDir` works on any K8s cluster, including Talos Linux (no FUSE,
  no CSI driver, no kernel modules needed).
- Avoids the POSIX-compatibility and consistency issues of S3 FUSE
  mounts (s3fs, geesefs) for mutable task workdirs.
- Simplest approach for the Nextflow-via-TES vertical slice where
  files are small `.command.*` scripts and logs.

**What we gave up:**
- Data is copied twice (S3 → emptyDir → S3) instead of accessed in
  place.
- Workdir size is limited by node disk space (not object storage).
- Large reference files would benefit from a read-only mounted volume
  (e.g. Mountpoint for S3 or CSI-S3).

**Risk if never addressed:**
- Cannot handle tasks with very large input/output datasets efficiently.
- Storage-intensive workflows may run out of ephemeral disk.

**When to address:**
- When tasks routinely need >10GB of input data.
- When adding large-reference-file support (read-only S3 mount).
- When CSI-S3 or JuiceFS is deployed to the cluster.

---

## TD-012: Output stage-out not yet implemented

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
The reconciler detects task completion and captures exit codes, but
does not yet upload output files from the workdir to S3. For the
Nextflow-via-nf-ga4gh path, Nextflow's TES plugin declares outputs
that point back to the S3 work directory — so Nextflow handles
output collection itself via the S3 URLs.

**Why:**
- nf-ga4gh writes `.command.out`, `.command.err`, `.exitcode` etc. as
  TES outputs with `url=s3://...`, meaning the TES server is expected
  to upload them. However, since the executor runs with emptyDir and
  the files are created locally, we need to implement stage-out.
- This is deferred until we have the stage-in path working end-to-end.

**Risk if never addressed:**
- TES output files won't be uploaded to S3 after completion.
- nf-ga4gh expects `.command.out` and `.command.err` to be available
  at the declared S3 URLs after task completion.

**When to address:**
- Before the first end-to-end Nextflow run (Batch D).
- This is the most critical remaining gap.

---

## TD-013: No pod security context on task Jobs

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
Task Jobs do not set `securityContext` on the executor container or
pod. The executor runs as whatever user the container image specifies.

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
  ARES, or WES attachment support in Batch D.
- Workflow source handling and workflow parameter normalization are partially
  hardcoded instead of hidden behind a reusable adapter interface.
- Only full 40-char commit SHAs are accepted as revisions; tags and branches
  are rejected for reproducibility.

**Risk if never addressed:**
- Source handling will become scattered and engine-specific.
- Future support for Git variants, object storage, ARES, DOS/DRS, and other
  engines will be harder to add cleanly.
- WES request compatibility will drift as more source-specific exceptions
  are added.

**When to address:**
- Before adding non-public Git sources (SSH, private repos).
- Before adding S3/TRS/ARES/DOS/DRS workflow sources.
- Before Snakemake support.
- When introducing a broader workflow-source adapter/plugin system.

---

## TD-015: Runner image Nextflow/plugin versions are pinned but not parameterized

**Decided:** 2026-03-26
**Status:** Active

**What we chose:**
The runner Dockerfile hardcodes Nextflow 24.10.4, nf-ga4gh 1.3.0, and
nf-amazon 2.9.0. Changing versions requires rebuilding the image.

**Why:**
- Reproducibility and simplicity for the first end-to-end slice.
- No need for runtime plugin resolution with network access.

**Risk if never addressed:**
- Cannot easily test new Nextflow/plugin versions without image rebuilds.
- Cannot support workflows that require specific Nextflow versions.

**When to address:**
- When a workflow requires a different Nextflow version.
- When adding support for user-specified engine versions.

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
