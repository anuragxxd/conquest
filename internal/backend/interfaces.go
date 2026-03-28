// Package backend defines the execution backend interfaces.
//
// The TES K8s backend is the first implementation, but the interfaces are
// designed to be engine-agnostic. A backend translates a TES task into
// infrastructure-specific execution (K8s Jobs, cloud batch, etc.) and
// reports state back.
package backend

import (
	"context"

	"github.com/anuragxxd/conquest/internal/domain"
)

type EnvVar struct {
	Name  string
	Value string
}

type RunnerSpec struct {
	Image   string
	Command []string
	Args    []string
	Env     []EnvVar
}

type RunSubmission struct {
	BackendRef string
}

// TaskBackend is the interface that execution backends must implement.
//
// The control plane (TES handler, reconciler) interacts with backends
// exclusively through this interface, keeping engine-specific and
// infrastructure-specific details behind the boundary.
type TaskBackend interface {
	// Submit creates the backend execution resources for the task.
	// It should set task.BackendRef to the infrastructure identifier
	// (e.g. K8s Job name). The task is already persisted in the DB
	// when Submit is called.
	Submit(ctx context.Context, task *domain.Task) error

	// Cancel requests cancellation of the backend execution.
	// For K8s: deletes the Job. Idempotent — must not error if the
	// backend resource is already gone.
	Cancel(ctx context.Context, task *domain.Task) error

	// Refresh queries the backend for the current state of a task
	// and returns a BackendStatus. Does not modify the task in the DB.
	Refresh(ctx context.Context, task *domain.Task) (*BackendStatus, error)
}

// BackendStatus is the backend's view of a task's current execution state.
// The reconciler uses this to update the domain task.
type BackendStatus struct {
	// Phase is the TES state the backend thinks this task is in.
	Phase domain.State

	// ExitCode from the executor container, if available.
	ExitCode *int32

	// Reason is a human-readable explanation of the current state
	// (e.g. "ImagePullBackOff", "OOMKilled", "Completed").
	Reason string

	// PodName is the K8s pod name, if applicable.
	PodName string

	// Logs is a snippet of stdout/stderr from the executor, if available.
	Logs string
}

// ---------------------------------------------------------------------------
// Run backend — for runner Jobs (Nextflow, Snakemake, etc.)
// ---------------------------------------------------------------------------

// RunBackend is the interface that run-level execution backends implement.
// It manages the lifecycle of a runner Job (e.g. Nextflow runner K8s Job).
type RunBackend interface {
	// SubmitRun creates the backend execution resources for a WES run.
	SubmitRun(ctx context.Context, run *domain.Run, spec *RunnerSpec) (*RunSubmission, error)

	// CancelRun requests cancellation of the backend execution for a run.
	CancelRun(ctx context.Context, run *domain.Run) error

	// RefreshRun queries the backend for the current state of a run's
	// runner Job and returns a BackendStatus.
	RefreshRun(ctx context.Context, run *domain.Run) (*BackendStatus, error)
}
