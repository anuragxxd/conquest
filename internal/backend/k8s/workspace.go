// Package k8s workspace strategy — backend-local abstraction for run/task
// workspace volumes. This file centralizes all workspace-mode branching so
// that jobspec builders and the run backend don't scatter mode checks.
package k8s

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/anuragxxd/conquest/internal/domain"
)

// WorkspaceConfig holds operator-supplied workspace settings, loaded from
// config.Config at startup and threaded into both RunBackend and Backend.
type WorkspaceConfig struct {
	Mode         string // "emptydir", "pvc", or "objectfs"
	AccessMode   string // "RWX" or "RWO" (pvc only)
	StorageClass string // optional; empty = cluster default (pvc only)
	Size         string // e.g. "10Gi"

	// ObjectFS-specific fields (reused from platform config).
	S3Bucket          string // e.g. "conquest"
	S3Endpoint        string // e.g. "http://minio.platform.svc.cluster.local:9000"
	LayoutVersion     string // e.g. "v1"
	StorageSecretName string // e.g. "storage-secrets"

	// ObjectFS CSI secret — the Yandex S3 CSI driver expects specific keys.
	ObjectFSCSISecretName      string // e.g. "csi-s3-secret"
	ObjectFSCSISecretNamespace string // e.g. "services"
}

// IsPVC returns true when the operator opted into shared PVC workspace.
func (wc WorkspaceConfig) IsPVC() bool {
	return wc.Mode == "pvc"
}

// IsObjectFS returns true when the operator opted into S3-mounted workspace.
func (wc WorkspaceConfig) IsObjectFS() bool {
	return wc.Mode == "objectfs"
}

// IsShared returns true for any shared-filesystem workspace mode.
func (wc WorkspaceConfig) IsShared() bool {
	return wc.IsPVC() || wc.IsObjectFS()
}

// ---------------------------------------------------------------------------
// Workspace Strategy interface
// ---------------------------------------------------------------------------

// WorkspaceStrategy abstracts the three workspace modes behind a common
// contract. The run backend and jobspec builders call into this interface
// instead of switching on config values directly.
type WorkspaceStrategy interface {
	// Provision creates any infrastructure required before the runner Job
	// starts (e.g. a PVC). Returns a WorkspaceBinding to persist on the run.
	// For emptydir this is a no-op returning nil.
	Provision(ctx context.Context, runID string) (*domain.WorkspaceBinding, error)

	// RunnerVolumes returns the volume and volumeMount entries for the
	// runner pod's /workspace mount. PVC modes return a PVC-backed volume
	// with subPath; emptydir returns the classic emptyDir.
	RunnerVolumes(binding *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount)

	// TaskVolumes returns the volume and volumeMount entries for a task pod's
	// workspace. The scope ("isolated" or "shared") and the computed mount
	// path control the subPath and mountPath.
	TaskVolumes(binding *domain.WorkspaceBinding, taskID, mountPath, scope string) (corev1.Volume, corev1.VolumeMount)

	// TaskAffinity returns pod affinity rules that must be applied to task
	// pods. Only RWO mode returns a non-nil affinity (co-locate with runner).
	TaskAffinity(binding *domain.WorkspaceBinding, runID string) *corev1.Affinity

	// RunnerEnv returns extra environment variables to inject into the runner
	// container (e.g. CONQUEST_SHARED_WORKSPACE=true for PVC modes).
	RunnerEnv() []corev1.EnvVar

	// Cleanup deletes infrastructure provisioned for a run (e.g. PVC).
	// Idempotent — must not error if resources are already gone.
	Cleanup(ctx context.Context, binding *domain.WorkspaceBinding) error
}

// NewWorkspaceStrategy builds the correct strategy from operator config.
func NewWorkspaceStrategy(client kubernetes.Interface, cfg WorkspaceConfig, namespace string, log *slog.Logger) WorkspaceStrategy {
	if cfg.IsObjectFS() {
		csiSecretNS := cfg.ObjectFSCSISecretNamespace
		if csiSecretNS == "" {
			csiSecretNS = namespace
		}
		return &objectFSWorkspace{
			client:              client,
			namespace:           namespace,
			size:                cfg.Size,
			s3Bucket:            cfg.S3Bucket,
			layoutVersion:       cfg.LayoutVersion,
			csiSecretName:       cfg.ObjectFSCSISecretName,
			csiSecretNamespace:  csiSecretNS,
			log:                 log,
		}
	}
	if !cfg.IsPVC() {
		return &emptyDirWorkspace{}
	}
	base := pvcWorkspaceBase{
		client:       client,
		namespace:    namespace,
		storageClass: cfg.StorageClass,
		size:         cfg.Size,
		log:          log,
	}
	if cfg.AccessMode == "RWO" {
		return &pvcWorkspaceRWO{pvcWorkspaceBase: base}
	}
	return &pvcWorkspaceRWX{pvcWorkspaceBase: base}
}

// ---------------------------------------------------------------------------
// EmptyDir strategy — preserves current behavior
// ---------------------------------------------------------------------------

type emptyDirWorkspace struct{}

func (e *emptyDirWorkspace) Provision(_ context.Context, _ string) (*domain.WorkspaceBinding, error) {
	return nil, nil // no-op
}

func (e *emptyDirWorkspace) RunnerVolumes(_ *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount) {
	return corev1.Volume{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}, corev1.VolumeMount{
			Name:      "workspace",
			MountPath: "/workspace",
		}
}

func (e *emptyDirWorkspace) TaskVolumes(_ *domain.WorkspaceBinding, _, mountPath, _ string) (corev1.Volume, corev1.VolumeMount) {
	return corev1.Volume{
			Name:         "workdir",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}, corev1.VolumeMount{
			Name:      "workdir",
			MountPath: mountPath,
		}
}

func (e *emptyDirWorkspace) TaskAffinity(_ *domain.WorkspaceBinding, _ string) *corev1.Affinity {
	return nil
}

func (e *emptyDirWorkspace) RunnerEnv() []corev1.EnvVar { return nil }

func (e *emptyDirWorkspace) Cleanup(_ context.Context, _ *domain.WorkspaceBinding) error {
	return nil
}

// ---------------------------------------------------------------------------
// Shared PVC base — common logic for RWX and RWO
// ---------------------------------------------------------------------------

type pvcWorkspaceBase struct {
	client       kubernetes.Interface
	namespace    string
	storageClass string
	size         string
	log          *slog.Logger
}

// claimName produces a deterministic, DNS-safe PVC name for a run.
func claimName(runID string) string {
	name := sanitizeK8sName("conquest-workspace-" + runID)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// createPVC creates a PVC with the given access mode.
func (b *pvcWorkspaceBase) createPVC(ctx context.Context, runID string, accessMode corev1.PersistentVolumeAccessMode) (string, error) {
	name := claimName(runID)
	qty, err := resource.ParseQuantity(b.size)
	if err != nil {
		return "", fmt.Errorf("parse workspace size %q: %w", b.size, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels: map[string]string{
				LabelRunID:   runID,
				LabelManaged: "conquest",
				"conquest.io/workspace-mode": string(accessMode),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}

	if b.storageClass != "" {
		pvc.Spec.StorageClassName = &b.storageClass
	}

	_, err = b.client.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create workspace PVC %s: %w", name, err)
	}

	b.log.Info("created workspace PVC",
		"pvc_name", name,
		"run_id", runID,
		"access_mode", string(accessMode),
		"size", b.size,
	)
	return name, nil
}

// deletePVC removes a workspace PVC. Ignores NotFound (idempotent).
func (b *pvcWorkspaceBase) deletePVC(ctx context.Context, claimName string) error {
	err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, claimName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete workspace PVC %s: %w", claimName, err)
	}
	return nil
}

// pvcRunnerVolumes builds the volume + mount for the runner container.
func pvcRunnerVolumes(binding *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount) {
	return corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: binding.ClaimName,
				},
			},
		}, corev1.VolumeMount{
			Name:      "workspace",
			MountPath: "/workspace",
			SubPath:   "shared",
		}
}

// pvcTaskVolumes builds the volume + mount for a task container.
// scope "shared" mounts subPath "shared" at /workspace so that absolute paths
// align with the runner checkout (e.g. /workspace/workflow/results/hello.txt
// maps to the same PVC path as the runner sees).
// scope "isolated" (default) mounts subPath "tasks/<taskID>" at the computed
// workdir mount path, preserving per-task isolation semantics.
func pvcTaskVolumes(binding *domain.WorkspaceBinding, taskID, mountPath, scope string) (corev1.Volume, corev1.VolumeMount) {
	subPath := "tasks/" + taskID
	effectiveMountPath := mountPath
	if scope == "shared" {
		subPath = "shared"
		// Mount at /workspace so task absolute paths match the runner.
		// The executor workdir (/workspace/workflow) is a subdirectory
		// that the runner already populated via git checkout.
		effectiveMountPath = "/workspace"
	}

	return corev1.Volume{
			Name: "workdir",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: binding.ClaimName,
				},
			},
		}, corev1.VolumeMount{
			Name:      "workdir",
			MountPath: effectiveMountPath,
			SubPath:   subPath,
		}
}

// sharedRunnerEnv returns the CONQUEST_SHARED_WORKSPACE env var for PVC modes.
func sharedRunnerEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "CONQUEST_SHARED_WORKSPACE", Value: "true"},
	}
}

// ---------------------------------------------------------------------------
// PVC RWX strategy
// ---------------------------------------------------------------------------

type pvcWorkspaceRWX struct {
	pvcWorkspaceBase
}

func (p *pvcWorkspaceRWX) Provision(ctx context.Context, runID string) (*domain.WorkspaceBinding, error) {
	name, err := p.createPVC(ctx, runID, corev1.ReadWriteMany)
	if err != nil {
		return nil, err
	}
	return &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  name,
		AccessMode: domain.WorkspaceAccessModeRWX,
	}, nil
}

func (p *pvcWorkspaceRWX) RunnerVolumes(binding *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount) {
	return pvcRunnerVolumes(binding)
}

func (p *pvcWorkspaceRWX) TaskVolumes(binding *domain.WorkspaceBinding, taskID, mountPath, scope string) (corev1.Volume, corev1.VolumeMount) {
	return pvcTaskVolumes(binding, taskID, mountPath, scope)
}

func (p *pvcWorkspaceRWX) TaskAffinity(_ *domain.WorkspaceBinding, _ string) *corev1.Affinity {
	return nil // RWX needs no scheduling constraints
}

func (p *pvcWorkspaceRWX) RunnerEnv() []corev1.EnvVar {
	return sharedRunnerEnv()
}

func (p *pvcWorkspaceRWX) Cleanup(ctx context.Context, binding *domain.WorkspaceBinding) error {
	if binding == nil || binding.ClaimName == "" {
		return nil
	}
	return p.deletePVC(ctx, binding.ClaimName)
}

// ---------------------------------------------------------------------------
// PVC RWO strategy — same mounting, but adds pod affinity for co-location
// ---------------------------------------------------------------------------

type pvcWorkspaceRWO struct {
	pvcWorkspaceBase
}

func (p *pvcWorkspaceRWO) Provision(ctx context.Context, runID string) (*domain.WorkspaceBinding, error) {
	name, err := p.createPVC(ctx, runID, corev1.ReadWriteOnce)
	if err != nil {
		return nil, err
	}
	return &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  name,
		AccessMode: domain.WorkspaceAccessModeRWO,
	}, nil
}

func (p *pvcWorkspaceRWO) RunnerVolumes(binding *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount) {
	return pvcRunnerVolumes(binding)
}

func (p *pvcWorkspaceRWO) TaskVolumes(binding *domain.WorkspaceBinding, taskID, mountPath, scope string) (corev1.Volume, corev1.VolumeMount) {
	return pvcTaskVolumes(binding, taskID, mountPath, scope)
}

// TaskAffinity returns a required pod affinity that co-locates the task pod
// on the same node as the runner pod for this run. This is necessary for RWO
// PVCs which can only be mounted on a single node.
func (p *pvcWorkspaceRWO) TaskAffinity(_ *domain.WorkspaceBinding, runID string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							LabelRunID:   runID,
							LabelJobType: "runner",
						},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}
}

func (p *pvcWorkspaceRWO) RunnerEnv() []corev1.EnvVar {
	return sharedRunnerEnv()
}

func (p *pvcWorkspaceRWO) Cleanup(ctx context.Context, binding *domain.WorkspaceBinding) error {
	if binding == nil || binding.ClaimName == "" {
		return nil
	}
	return p.deletePVC(ctx, binding.ClaimName)
}

// ---------------------------------------------------------------------------
// ObjectFS strategy — S3-backed workspace via Yandex S3 CSI + GeeseFS
// ---------------------------------------------------------------------------

const (
	// objectFSCSIDriver is the registered CSI driver name.
	objectFSCSIDriver = "ru.yandex.s3.csi"

	// objectFSMounter selects GeeseFS inside the CSI driver.
	objectFSMounter = "geesefs"

	// objectFSMountOptions are hardcoded for Talos (no systemd) with
	// relaxed permissions for non-root task containers.
	objectFSMountOptions = "--no-systemd --memory-limit 1000 --dir-mode 0777 --file-mode 0666"
)

type objectFSWorkspace struct {
	client             kubernetes.Interface
	namespace          string
	size               string // e.g. "10Gi"
	s3Bucket           string // e.g. "conquest"
	layoutVersion      string // e.g. "v1"
	csiSecretName      string // e.g. "csi-s3-secret"
	csiSecretNamespace string // e.g. "services"
	log                *slog.Logger
}

// pvName produces a deterministic, DNS-safe PV name for a run.
func pvName(runID string) string {
	name := sanitizeK8sName("conquest-objectfs-" + runID)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// objectFSClaimName produces a deterministic PVC name for objectfs.
func objectFSClaimName(runID string) string {
	return pvName(runID) // PV and PVC share the same name for clarity.
}

// volumeHandle builds the S3 prefix that becomes the mount root.
// Layout: <bucket>/<version>/workflow-runs/<runID>/workspace
func (o *objectFSWorkspace) volumeHandle(runID string) string {
	return o.s3Bucket + "/" + o.layoutVersion + "/workflow-runs/" + runID + "/workspace"
}

func (o *objectFSWorkspace) Provision(ctx context.Context, runID string) (*domain.WorkspaceBinding, error) {
	pv := pvName(runID)
	claim := objectFSClaimName(runID)
	qty, err := resource.ParseQuantity(o.size)
	if err != nil {
		return nil, fmt.Errorf("parse objectfs workspace size %q: %w", o.size, err)
	}

	// 1. Create static PV.
	emptyStorageClass := ""
	pvObj := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pv,
			Labels: map[string]string{
				LabelRunID:   runID,
				LabelManaged: "conquest",
				"conquest.io/workspace-mode": "objectfs",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: emptyStorageClass,
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: qty,
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Namespace: o.namespace,
				Name:      claim,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       objectFSCSIDriver,
					VolumeHandle: o.volumeHandle(runID),
					VolumeAttributes: map[string]string{
						"capacity": fmt.Sprintf("%d", qty.Value()),
						"mounter":  objectFSMounter,
						"options":  objectFSMountOptions,
					},
					ControllerPublishSecretRef: &corev1.SecretReference{
						Name:      o.csiSecretName,
						Namespace: o.csiSecretNamespace,
					},
					NodePublishSecretRef: &corev1.SecretReference{
						Name:      o.csiSecretName,
						Namespace: o.csiSecretNamespace,
					},
					NodeStageSecretRef: &corev1.SecretReference{
						Name:      o.csiSecretName,
						Namespace: o.csiSecretNamespace,
					},
				},
			},
		},
	}

	if _, err := o.client.CoreV1().PersistentVolumes().Create(ctx, pvObj, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create objectfs PV %s: %w", pv, err)
	}

	// 2. Create PVC bound to the static PV.
	pvcObj := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claim,
			Namespace: o.namespace,
			Labels: map[string]string{
				LabelRunID:   runID,
				LabelManaged: "conquest",
				"conquest.io/workspace-mode": "objectfs",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &emptyStorageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
			VolumeName: pv,
		},
	}

	if _, err := o.client.CoreV1().PersistentVolumeClaims(o.namespace).Create(ctx, pvcObj, metav1.CreateOptions{}); err != nil {
		// Best-effort cleanup of the PV we just created.
		_ = o.client.CoreV1().PersistentVolumes().Delete(ctx, pv, metav1.DeleteOptions{})
		return nil, fmt.Errorf("create objectfs PVC %s: %w", claim, err)
	}

	o.log.Info("created objectfs workspace",
		"pv_name", pv,
		"pvc_name", claim,
		"run_id", runID,
		"volume_handle", o.volumeHandle(runID),
		"size", o.size,
	)

	return &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindObjectFS,
		PVName:     pv,
		ClaimName:  claim,
		AccessMode: domain.WorkspaceAccessModeRWX,
	}, nil
}

func (o *objectFSWorkspace) RunnerVolumes(binding *domain.WorkspaceBinding) (corev1.Volume, corev1.VolumeMount) {
	// ObjectFS mounts at /workspace with no subPath — the S3 prefix IS the
	// run-scoped root, so /workspace/foo maps to s3://.../workspace/foo.
	return corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: binding.ClaimName,
				},
			},
		}, corev1.VolumeMount{
			Name:      "workspace",
			MountPath: "/workspace",
		}
}

func (o *objectFSWorkspace) TaskVolumes(binding *domain.WorkspaceBinding, taskID, mountPath, scope string) (corev1.Volume, corev1.VolumeMount) {
	effectiveMountPath := mountPath
	subPath := "tasks/" + taskID
	if scope == "shared" {
		subPath = ""
		effectiveMountPath = "/workspace"
	}

	vm := corev1.VolumeMount{
		Name:      "workdir",
		MountPath: effectiveMountPath,
	}
	if subPath != "" {
		vm.SubPath = subPath
	}

	return corev1.Volume{
		Name: "workdir",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: binding.ClaimName,
			},
		},
	}, vm
}

func (o *objectFSWorkspace) TaskAffinity(_ *domain.WorkspaceBinding, _ string) *corev1.Affinity {
	return nil // ObjectFS is RWX — no scheduling constraints needed.
}

func (o *objectFSWorkspace) RunnerEnv() []corev1.EnvVar {
	return sharedRunnerEnv()
}

// Cleanup deletes the PVC and PV for an objectfs workspace.
// S3 objects are intentionally preserved — data lifecycle is separate.
func (o *objectFSWorkspace) Cleanup(ctx context.Context, binding *domain.WorkspaceBinding) error {
	if binding == nil {
		return nil
	}

	// Delete PVC first (K8s PVC protection defers actual deletion until
	// pods have unmounted).
	if binding.ClaimName != "" {
		err := o.client.CoreV1().PersistentVolumeClaims(o.namespace).Delete(ctx, binding.ClaimName, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete objectfs PVC %s: %w", binding.ClaimName, err)
		}
	}

	// Delete PV.
	if binding.PVName != "" {
		err := o.client.CoreV1().PersistentVolumes().Delete(ctx, binding.PVName, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete objectfs PV %s: %w", binding.PVName, err)
		}
	}

	o.log.Info("cleaned up objectfs workspace",
		"pv_name", binding.PVName,
		"pvc_name", binding.ClaimName,
	)
	return nil
}
