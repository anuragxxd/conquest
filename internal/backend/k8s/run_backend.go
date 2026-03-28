package k8s

import (
	"context"
	"fmt"
	"log/slog"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

// RunBackend implements backend.RunBackend using Kubernetes Jobs.
type RunBackend struct {
	client    kubernetes.Interface
	cfg       RunConfig
	workspace WorkspaceStrategy
	log       *slog.Logger
}

// RunConfig holds configuration for the runner K8s backend.
type RunConfig struct {
	Namespace               string
	ServiceAccount          string
	StorageSecretName       string
	ImagePullSecrets        []string
	EphemeralStorageRequest string
	EphemeralStorageLimit   string
	Workspace               WorkspaceConfig
}

var _ backend.RunBackend = (*RunBackend)(nil)

// NewRunBackend creates a new run-level K8s backend.
func NewRunBackend(client kubernetes.Interface, cfg RunConfig, log *slog.Logger) *RunBackend {
	ws := NewWorkspaceStrategy(client, cfg.Workspace, cfg.Namespace, log)
	return &RunBackend{client: client, cfg: cfg, workspace: ws, log: log}
}

// SubmitRun creates a workspace PVC (if configured), then a K8s Job for the
// runner. If the Job fails to create after PVC provisioning, the PVC is
// cleaned up best-effort.
func (b *RunBackend) SubmitRun(ctx context.Context, run *domain.Run, spec *backend.RunnerSpec) (*backend.RunSubmission, error) {
	// 1. Provision workspace (no-op for emptydir).
	binding, err := b.workspace.Provision(ctx, run.RunID)
	if err != nil {
		return nil, fmt.Errorf("provision workspace: %w", err)
	}

	// 2. Build runner Job spec with workspace awareness.
	job, err := buildRunnerJobSpec(run, b.cfg, spec, b.workspace, binding)
	if err != nil {
		// Best-effort cleanup of the PVC we just created.
		if cleanupErr := b.workspace.Cleanup(ctx, binding); cleanupErr != nil {
			b.log.Error("failed to cleanup workspace after jobspec build failure",
				"run_id", run.RunID,
				"err", cleanupErr,
			)
		}
		return nil, fmt.Errorf("build runner job spec: %w", err)
	}

	// 3. Create the K8s Job.
	created, err := b.client.BatchV1().Jobs(b.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		// Best-effort cleanup of the PVC we just created.
		if cleanupErr := b.workspace.Cleanup(ctx, binding); cleanupErr != nil {
			b.log.Error("failed to cleanup workspace after job creation failure",
				"run_id", run.RunID,
				"err", cleanupErr,
			)
		}
		return nil, fmt.Errorf("create runner k8s job: %w", err)
	}

	b.log.Info("submitted runner k8s job",
		"run_id", run.RunID,
		"job_name", created.Name,
		"namespace", b.cfg.Namespace,
		"workspace_mode", b.cfg.Workspace.Mode,
	)
	return &backend.RunSubmission{
		BackendRef: created.Name,
		Workspace:  binding,
	}, nil
}

// CancelRun deletes the K8s Job for the runner.
func (b *RunBackend) CancelRun(ctx context.Context, run *domain.Run) error {
	if run.BackendRef == "" {
		return nil
	}

	propagation := metav1.DeletePropagationBackground
	err := b.client.BatchV1().Jobs(b.cfg.Namespace).Delete(ctx, run.BackendRef, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete runner k8s job %s: %w", run.BackendRef, err)
	}

	b.log.Info("cancelled runner k8s job",
		"run_id", run.RunID,
		"job_name", run.BackendRef,
	)
	return nil
}

// RefreshRun queries K8s for the runner Job's current state.
func (b *RunBackend) RefreshRun(ctx context.Context, run *domain.Run) (*backend.BackendStatus, error) {
	if run.BackendRef == "" {
		return &backend.BackendStatus{
			Phase:  domain.StateQueued,
			Reason: "no backend ref yet",
		}, nil
	}

	job, err := b.client.BatchV1().Jobs(b.cfg.Namespace).Get(ctx, run.BackendRef, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &backend.BackendStatus{
				Phase:  domain.StateSystemError,
				Reason: "runner k8s job not found",
			}, nil
		}
		return nil, fmt.Errorf("get runner k8s job %s: %w", run.BackendRef, err)
	}

	return b.mapRunnerJobToStatus(ctx, job)
}

// WorkspaceStrategy returns the workspace strategy for use by the reconciler
// during cleanup.
func (b *RunBackend) WorkspaceStrategy() WorkspaceStrategy {
	return b.workspace
}

func (b *RunBackend) mapRunnerJobToStatus(ctx context.Context, job *batchv1.Job) (*backend.BackendStatus, error) {
	status := &backend.BackendStatus{}

	// Check job-level conditions.
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete:
			status.Phase = domain.StateComplete
			status.Reason = "runner completed"
			b.enrichRunnerFromPods(ctx, job, status)
			return status, nil
		case batchv1.JobFailed:
			status.Phase = domain.StateExecutorError
			status.Reason = fmt.Sprintf("runner failed: %s", cond.Message)
			b.enrichRunnerFromPods(ctx, job, status)
			return status, nil
		}
	}

	if job.Status.Active > 0 {
		status.Phase = domain.StateRunning
		status.Reason = "runner pod active"
		b.enrichRunnerFromPods(ctx, job, status)
		return status, nil
	}

	if job.Status.Succeeded > 0 {
		status.Phase = domain.StateComplete
		status.Reason = "runner succeeded"
		b.enrichRunnerFromPods(ctx, job, status)
		return status, nil
	}

	if job.Status.Failed > 0 {
		status.Phase = domain.StateExecutorError
		status.Reason = "runner failed"
		b.enrichRunnerFromPods(ctx, job, status)
		return status, nil
	}

	status.Phase = domain.StateInitializing
	status.Reason = "runner pending"
	return status, nil
}

func (b *RunBackend) enrichRunnerFromPods(ctx context.Context, job *batchv1.Job, status *backend.BackendStatus) {
	pods, err := b.client.CoreV1().Pods(b.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
	})
	if err != nil || len(pods.Items) == 0 {
		return
	}

	pod := pods.Items[0]
	status.PodName = pod.Name

	// Check for image pull errors.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				status.Phase = domain.StateSystemError
				status.Reason = fmt.Sprintf("runner container %s: %s", cs.Name, reason)
				return
			}
		}
	}

	// Get exit code from runner container.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "runner" && cs.State.Terminated != nil {
			exitCode := int32(cs.State.Terminated.ExitCode)
			status.ExitCode = &exitCode
			if exitCode == 0 {
				status.Phase = domain.StateComplete
				status.Reason = "runner completed successfully"
			} else {
				status.Phase = domain.StateExecutorError
				status.Reason = fmt.Sprintf("runner exited with code %d", exitCode)
			}
			return
		}
	}
}
