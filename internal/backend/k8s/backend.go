package k8s

import (
	"context"
	"encoding/json"
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

// Backend implements backend.TaskBackend using Kubernetes Jobs.
type Backend struct {
	client    kubernetes.Interface
	cfg       Config
	workspace WorkspaceStrategy
	log       *slog.Logger
}

// Ensure Backend satisfies the interface.
var _ backend.TaskBackend = (*Backend)(nil)

// NewBackend creates a K8s backend.
func NewBackend(client kubernetes.Interface, cfg Config, log *slog.Logger) *Backend {
	ws := NewWorkspaceStrategy(client, cfg.Workspace, cfg.Namespace, log)
	return &Backend{
		client:    client,
		cfg:       cfg,
		workspace: ws,
		log:       log,
	}
}

// Submit creates a K8s Job for the given task.
func (b *Backend) Submit(ctx context.Context, task *domain.Task) error {
	job, err := buildJobSpec(task, b.cfg, b.workspace)
	if err != nil {
		return fmt.Errorf("build job spec: %w", err)
	}

	created, err := b.client.BatchV1().Jobs(b.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create k8s job: %w", err)
	}

	task.BackendRef = created.Name

	b.log.Info("submitted k8s job",
		"task_id", task.TESTaskID,
		"job_name", created.Name,
		"namespace", b.cfg.Namespace,
	)

	return nil
}

// Cancel deletes the K8s Job for the given task.
func (b *Backend) Cancel(ctx context.Context, task *domain.Task) error {
	if task.BackendRef == "" {
		return nil // nothing to cancel
	}

	propagation := metav1.DeletePropagationBackground
	err := b.client.BatchV1().Jobs(b.cfg.Namespace).Delete(ctx, task.BackendRef, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete k8s job %s: %w", task.BackendRef, err)
	}

	b.log.Info("cancelled k8s job",
		"task_id", task.TESTaskID,
		"job_name", task.BackendRef,
	)

	return nil
}

// Refresh queries the K8s API for the current state of a task's Job.
func (b *Backend) Refresh(ctx context.Context, task *domain.Task) (*backend.BackendStatus, error) {
	if task.BackendRef == "" {
		return &backend.BackendStatus{
			Phase:  domain.StateQueued,
			Reason: "no backend ref yet",
		}, nil
	}

	job, err := b.client.BatchV1().Jobs(b.cfg.Namespace).Get(ctx, task.BackendRef, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Job was deleted (cleanup or external). If task was not terminal,
			// this is a system error.
			return &backend.BackendStatus{
				Phase:  domain.StateSystemError,
				Reason: "k8s job not found (deleted externally?)",
			}, nil
		}
		return nil, fmt.Errorf("get k8s job %s: %w", task.BackendRef, err)
	}

	return b.mapJobToStatus(ctx, job)
}

// mapJobToStatus derives a TES state from a K8s Job + its Pods.
func (b *Backend) mapJobToStatus(ctx context.Context, job *batchv1.Job) (*backend.BackendStatus, error) {
	status := &backend.BackendStatus{}

	// Check job-level conditions first.
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete:
			status.Phase = domain.StateComplete
			status.Reason = "job completed"
		case batchv1.JobFailed:
			status.Phase = domain.StateSystemError
			status.Reason = fmt.Sprintf("job failed: %s", cond.Message)
		}
	}

	// If we got a terminal state from conditions, refine with pod info.
	if status.Phase.IsTerminal() {
		b.enrichFromPods(ctx, job, status)
		return status, nil
	}

	// Non-terminal: check active/succeeded/failed counts.
	if job.Status.Active > 0 {
		// Check if init container is still running or main container started.
		b.enrichFromPods(ctx, job, status)
		if status.Phase == "" {
			status.Phase = domain.StateRunning
			status.Reason = "pod active"
		}
		return status, nil
	}

	if job.Status.Succeeded > 0 {
		status.Phase = domain.StateComplete
		status.Reason = "job succeeded"
		b.enrichFromPods(ctx, job, status)
		return status, nil
	}

	if job.Status.Failed > 0 {
		status.Phase = domain.StateSystemError
		status.Reason = "job failed"
		b.enrichFromPods(ctx, job, status)
		return status, nil
	}

	// No active, succeeded, or failed — either still being scheduled or
	// already stuck in a terminal waiting state like InvalidImageName.
	b.enrichFromPods(ctx, job, status)
	if status.Phase == "" {
		status.Phase = domain.StateInitializing
		status.Reason = "job pending"
	}
	return status, nil
}

// enrichFromPods reads pod-level details to get exit code and pod name.
func (b *Backend) enrichFromPods(ctx context.Context, job *batchv1.Job, status *backend.BackendStatus) {
	pods, err := b.client.CoreV1().Pods(b.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
	})
	if err != nil || len(pods.Items) == 0 {
		return
	}

	pod := pods.Items[0]
	status.PodName = pod.Name

	// Check pod phase for more detail.
	switch pod.Status.Phase {
	case corev1.PodPending:
		// Check for terminal waiting conditions before treating the pod as
		// merely pending. These states will never become runnable without a
		// control-plane change, so surfacing them as terminal task errors
		// avoids runners hanging indefinitely.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				if waitingReasonIsTerminal(reason) {
					status.Phase = domain.StateSystemError
					status.Reason = fmt.Sprintf("container %s: %s", cs.Name, reason)
					return
				}
			}
		}
		// Check init container statuses too.
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				if waitingReasonIsTerminal(reason) {
					status.Phase = domain.StateSystemError
					status.Reason = fmt.Sprintf("init container %s: %s", cs.Name, reason)
					return
				}
			}
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
				status.Phase = domain.StateSystemError
				status.Reason = fmt.Sprintf("init container %s failed: exit %d", cs.Name, cs.State.Terminated.ExitCode)
				return
			}
		}
		if status.Phase == "" {
			status.Phase = domain.StateInitializing
			status.Reason = "pod pending"
		}

	case corev1.PodRunning:
		// Check if init containers completed and main is running.
		if status.Phase == "" {
			status.Phase = domain.StateRunning
			status.Reason = "pod running"
		}

	case corev1.PodSucceeded, corev1.PodFailed:
		// Extract exit code from the executor container.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == "executor" && cs.State.Terminated != nil {
				exitCode := int32(cs.State.Terminated.ExitCode)
				status.ExitCode = &exitCode
				if exitCode == 0 {
					status.Phase = domain.StateComplete
					status.Reason = "executor completed successfully"
				} else {
					status.Phase = domain.StateExecutorError
					status.Reason = fmt.Sprintf("executor exited with code %d", exitCode)
				}
				return
			}
		}
		// Check init container failures (stage-in failed).
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
				status.Phase = domain.StateSystemError
				status.Reason = fmt.Sprintf("init container %s failed: exit %d (%s)",
					cs.Name, cs.State.Terminated.ExitCode, cs.State.Terminated.Reason)
				return
			}
		}
	}

	// Persist pod-level info as backend state.
	if bs, err := json.Marshal(map[string]any{
		"pod_name":  pod.Name,
		"pod_phase": string(pod.Status.Phase),
	}); err == nil {
		status.Logs = string(bs) // re-use Logs field for backend detail
	}
}

func waitingReasonIsTerminal(reason string) bool {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "ErrImageNeverPull":
		return true
	default:
		return false
	}
}
