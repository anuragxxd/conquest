# ObjectFS Plan for Conquest

## Goal

Add a first-class Conquest workspace mode:

```bash
CONQUEST_WORKSPACE_MODE=objectfs
```

The user should not choose or know the backend. Internally, Conquest will provide a transparent S3-compatible mounted filesystem using GeeseFS through the Yandex S3 CSI driver.

The bucket layout must remain human-readable:

```text
/workspace/results/a.bam
=> s3://<bucket>/v1/workflow-runs/<runID>/workspace/results/a.bam
```

No JuiceFS-style opaque chunk layout.

## Current environment assessment

Preview cluster appears suitable for testing.

Observed:

- Kubernetes server: v1.34.0
- Node OS: Talos v1.10.3
- Kernel: 6.12.28-talos
- Runtime: containerd 2.0.5
- Existing CSI driver: democratic-csi local-hostpath
- Default StorageClass: `standard`
- Conquest namespace: `services`
- Conquest is Helm-managed.
- Conquest currently uses `CONQUEST_WORKSPACE_MODE=pvc`, `CONQUEST_WORKSPACE_ACCESS_MODE=RWO`, `CONQUEST_WORKSPACE_STORAGE_CLASS=standard`.
- MinIO service exists at `minio.platform.svc.cluster.local:9000`.
- `storage-secrets` exists in `services`.
- Privileged CSI daemonsets are already allowed in the cluster via the `democratic-csi` namespace.
- Current Conquest service account can create Jobs and PVCs but cannot create cluster-scoped PVs.

Conclusion: objectfs is testable here after installing/bundling objectfs CSI infrastructure and adding the required RBAC.

## Design decision

Expose only:

```bash
CONQUEST_WORKSPACE_MODE=objectfs
```

Do not expose user-facing backend/mounter choices.

Internally hardcode:

- CSI driver: `ru.yandex.s3.csi`
- Mounter: `geesefs`
- Initial mount options:
  - `--no-systemd`
  - `--memory-limit 1000`
  - `--dir-mode 0777`
  - `--file-mode 0666`

`--no-systemd` is important because the cluster runs Talos, not a traditional systemd host.

## Why static PVs instead of dynamic PVC-only provisioning

The Yandex S3 CSI driver supports dynamic provisioning, but dynamic provisioning creates bucket/prefix names derived from volume IDs. That would keep objects browsable but not nicely run-scoped.

For readable Conquest layout, use static PVs created by Conquest:

```text
volumeHandle: <bucket>/v1/workflow-runs/<runID>/workspace
```

This gives exact S3 layout control.

## Code changes

### 1. Config

Update `internal/config/config.go`.

Current workspace validation accepts only `emptydir` and `pvc` at `internal/config/config.go:167`.

Add:

```text
objectfs
```

Add internal config fields as needed, but keep user-facing configuration minimal. Reuse:

- `CONQUEST_S3_BUCKET`
- `CONQUEST_S3_ENDPOINT`
- `CONQUEST_S3_REGION`
- `CONQUEST_STORAGE_SECRET_NAME`
- `CONQUEST_WORKSPACE_SIZE`

### 2. Domain model

Update `internal/domain/workspace.go`.

Currently only:

- `WorkspaceKindNone`
- `WorkspaceKindPVC`

Add:

```go
WorkspaceKindObjectFS WorkspaceKind = "objectfs"
```

Extend `WorkspaceBinding` to store:

- `PVName`
- `ClaimName`
- `S3Prefix` or `VolumeHandle`

Also replace or supplement `IsPVC()` with something like:

```go
RequiresWorkspaceCleanup()
```

because objectfs also needs Kubernetes cleanup.

### 3. Workspace strategy

Update `internal/backend/k8s/workspace.go`.

Add:

```go
type objectFSWorkspace struct { ... }
```

Behavior:

- `Provision(runID)` creates:
  - a static PersistentVolume
  - a matching PersistentVolumeClaim
- `RunnerVolumes()` mounts claim at `/workspace`
- `TaskVolumes()` follows current semantics:
  - shared scope => `/workspace`
  - isolated scope => per-task subpath if compatible
- `Cleanup()` deletes PVC and PV, but does not delete S3 objects.

Current seam is `WorkspaceStrategy` at `internal/backend/k8s/workspace.go:38`.

### 4. Static PV shape

Use Yandex CSI static provisioning pattern:

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: conquest-objectfs-<runID>
spec:
  storageClassName: ""
  capacity:
    storage: 10Gi
  accessModes:
    - ReadWriteMany
  claimRef:
    namespace: services
    name: conquest-objectfs-<runID>
  csi:
    driver: ru.yandex.s3.csi
    controllerPublishSecretRef:
      name: storage-secrets
      namespace: services
    nodePublishSecretRef:
      name: storage-secrets
      namespace: services
    nodeStageSecretRef:
      name: storage-secrets
      namespace: services
    volumeAttributes:
      capacity: 10Gi
      mounter: geesefs
      options: --no-systemd --memory-limit 1000 --dir-mode 0777 --file-mode 0666
    volumeHandle: conquest/v1/workflow-runs/<runID>/workspace
```

Then create a PVC with `storageClassName: ""`.

### 5. RBAC

Current chart RBAC only grants namespaced PVC permissions at `deploy/charts/conquest/templates/rbac.yaml:21`.

Add cluster-scoped permissions for PV management:

- create
- get
- list
- watch
- delete

Prefer a dedicated ClusterRole/ClusterRoleBinding gated behind:

```yaml
objectfs:
  enabled: true
```

### 6. Helm chart bundling

Conquest should ship objectfs support, not require users to install Yandex CSI manually.

Add chart values:

```yaml
objectfs:
  enabled: false
  installCSI: true
  namespace: conquest-objectfs
  storageSize: 10Gi
  geesefs:
    memoryLimitMb: 1000
    options:
      - --no-systemd
      - --dir-mode 0777
      - --file-mode 0666
```

When enabled, chart installs:

- objectfs namespace with privileged Pod Security labels
- Yandex S3 CSI controller/node components
- RBAC
- CSI secret wiring using existing S3 credentials
- optional StorageClass for manual debugging

Implementation detail may mention GeeseFS in internal docs, but product docs should call it “Conquest objectfs”.

### 7. Cleanup semantics

Current run cleanup only cleans PVC-backed workspaces at `internal/reconciler/run_reconciler.go:191`.

Change cleanup to handle objectfs bindings too.

Important: deleting PV/PVC should unmount and clean Kubernetes objects only. It should not delete S3 run data. S3 lifecycle/Conquest run deletion should handle object deletion separately.

### 8. SubPath risk

Current PVC task isolation uses subPath in `internal/backend/k8s/workspace.go:226`.

Need spike:

- Does Yandex CSI + GeeseFS support Kubernetes `subPath` correctly?
- Does `tasks/<taskID>` need to exist before pod mount?

If subPath fails, fallback:

- mount full run objectfs volume into task pods
- use engine-provided unique workdirs where possible
- or create per-task static PV/PVC for isolated tasks

## Test plan

### Cluster smoke test

After chart install:

1. Confirm CSI driver registered:

```bash
kubectl get csidrivers
```

2. Create manual static PV/PVC against:

```text
conquest/v1/objectfs-smoke/
```

3. Run test pod writing:

```text
/volume/hello.txt
```

4. Verify object appears in MinIO as:

```text
v1/objectfs-smoke/hello.txt
```

5. Test:

- write
- read
- rename
- directory listing
- delete
- many small files
- large sequential file

### Conquest smoke test

1. Deploy Conquest with:

```bash
CONQUEST_WORKSPACE_MODE=objectfs
```

2. Submit minimal workflow.
3. Verify runner and task pods mount objectfs.
4. Verify S3 object layout under:

```text
v1/workflow-runs/<runID>/workspace/
```

5. Verify terminal run cleans PV/PVC but leaves S3 objects.

### Workflow compatibility tests

Run:

- small Nextflow workflow
- WDL miniwdl workflow
- Snakemake workflow
- task writing many small files
- task writing large FASTQ/BAM-like file
- task using temp file + rename pattern
- interrupted/failed task

## Risks

- Talos may not expose FUSE as expected. Existing CSI setup suggests privileged storage plugins work, but `/dev/fuse` must be validated by smoke test.
- GeeseFS is POSIX-ish, not a real POSIX filesystem.
- Concurrent writes to the same object from multiple pods are unsafe.
- Metadata semantics, symlinks, custom mtimes, and permissions are imperfect on standard S3/MinIO.
- Static PVs require cluster-scoped RBAC for Conquest.
- Mount propagation and privileged CSI components are required.
- Kubernetes subPath behavior with S3 CSI must be verified.

## Rollback

Keep existing `emptydir` and `pvc` modes unchanged.

Gate objectfs behind:

```bash
CONQUEST_WORKSPACE_MODE=objectfs
```

Gate chart CSI installation behind:

```yaml
objectfs:
  enabled: true
```

If objectfs fails in preview, revert Conquest deployment to current PVC mode:

```bash
CONQUEST_WORKSPACE_MODE=pvc
CONQUEST_WORKSPACE_ACCESS_MODE=RWO
CONQUEST_WORKSPACE_STORAGE_CLASS=standard
```

## Recommendation

Proceed with a two-step implementation:

1. Manual cluster smoke test using Yandex S3 CSI + GeeseFS against MinIO.
2. If smoke test passes, implement first-class `objectfs` mode in Conquest and bundle the CSI installation in the Helm chart.
