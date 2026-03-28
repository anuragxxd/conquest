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
	Mode         string // "emptydir" or "pvc"
	AccessMode   string // "RWX" or "RWO"
	StorageClass string // optional; empty = cluster default
	Size         string // e.g. "10Gi"
}

// IsPVC returns true when the operator opted into shared PVC workspace.
func (wc WorkspaceConfig) IsPVC() bool {
	return wc.Mode == "pvc"
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
