package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

// WorkspaceCleaner is the subset of the workspace strategy interface needed
// by the reconciler for run cleanup. This avoids importing the k8s package
// directly, keeping the reconciler backend-agnostic.
type WorkspaceCleaner interface {
	Cleanup(ctx context.Context, binding *domain.WorkspaceBinding) error
}

// RunReconciler periodically polls active runs and syncs their state
// from the runner backend (K8s Jobs).
type RunReconciler struct {
	runs      repository.RunStore
	events    repository.EventStore
	backend   backend.RunBackend
	workspace WorkspaceCleaner // nil-safe; no-op if not set
	cfg       Config
	log       *slog.Logger
}

// NewRunReconciler creates a RunReconciler.
func NewRunReconciler(
	runs repository.RunStore,
	events repository.EventStore,
	be backend.RunBackend,
	cfg Config,
	log *slog.Logger,
) *RunReconciler {
	return &RunReconciler{
		runs:    runs,
		events:  events,
		backend: be,
		cfg:     cfg,
		log:     log,
	}
}

// SetWorkspaceCleaner configures the workspace cleanup strategy.
// Must be called before Run() if workspace cleanup is desired.
func (r *RunReconciler) SetWorkspaceCleaner(wc WorkspaceCleaner) {
	r.workspace = wc
}

// Run starts the run reconciliation loop. Blocks until ctx is cancelled.
func (r *RunReconciler) Run(ctx context.Context) {
	r.log.Info("run reconciler started",
		"poll_interval", r.cfg.PollInterval,
		"batch_size", r.cfg.BatchSize,
	)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("run reconciler stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

func (r *RunReconciler) reconcileOnce(ctx context.Context) {
	runs, err := r.runs.ListActiveRuns(ctx, r.cfg.BatchSize)
	if err != nil {
		r.log.Error("failed to list active runs", "err", err)
		return
	}

	if len(runs) == 0 {
		return
	}

	r.log.Debug("reconciling active runs", "count", len(runs))

	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		r.reconcileRun(ctx, run)
	}
}

func (r *RunReconciler) reconcileRun(ctx context.Context, run *domain.Run) {
	status, err := r.backend.RefreshRun(ctx, run)
	if err != nil {
		r.log.Error("failed to refresh run",
			"run_id", run.RunID,
			"err", err,
		)
		return
	}

	// No state change needed.
	if status.Phase == run.State {
		return
	}

	// For runs in CANCELING, map terminal backend states to CANCELED.
	targetState := status.Phase
	if run.State == domain.StateCanceling && targetState.IsTerminal() {
		targetState = domain.StateCanceled
	}

	// Validate the transition.
	if err := domain.ValidateRunTransition(run.State, targetState); err != nil {
		r.log.Warn("invalid run state transition from backend",
			"run_id", run.RunID,
			"from", run.State,
			"to", targetState,
			"reason", status.Reason,
			"err", err,
		)
		return
	}

	// Update state in the database.
	if err := r.runs.UpdateState(ctx, run.RunID, run.State, targetState); err != nil {
		r.log.Error("failed to update run state",
			"run_id", run.RunID,
			"from", run.State,
			"to", targetState,
			"err", err,
		)
		return
	}

	r.log.Info("run state updated",
		"run_id", run.RunID,
		"from", run.State,
		"to", targetState,
		"reason", status.Reason,
		"pod", status.PodName,
	)

	// Update backend state blob.
	if status.PodName != "" {
		bs, _ := json.Marshal(map[string]any{
			"pod_name": status.PodName,
			"reason":   status.Reason,
		})
		if err := r.runs.SetBackendState(ctx, run.RunID, bs); err != nil {
			r.log.Error("failed to persist run backend state",
				"run_id", run.RunID,
				"err", err,
			)
		}
	}

	// Append event.
	payload, _ := json.Marshal(domain.StateChangePayload{
		FromState: run.State,
		ToState:   targetState,
		Reason:    status.Reason,
	})
	event := domain.NewEvent(run.RunID, "", domain.EventRunStateChanged, payload)
	if err := r.events.Append(ctx, event); err != nil {
		r.log.Error("failed to append run state change event",
			"run_id", run.RunID,
			"err", err,
		)
	}

	// Cleanup workspace PVC when run enters a terminal state.
	if targetState.IsTerminal() {
		r.cleanupWorkspace(ctx, run, targetState)
	}
}

// cleanupWorkspace releases per-run workspace resources (PVC and optionally PV)
// when a run reaches a terminal state. Failures are logged but do not block
// the terminal state transition.
//
// Ordering matters. Kubernetes `pvc-protection` defers PVC deletion until every
// Pod that *references* the claim is deleted — not merely until those Pods have
// terminated or unmounted. Completed runner and task Pods therefore keep the
// finalizer held. Deleting the Jobs first (Background propagation) lets the Pods
// be garbage collected so the PVC and its static PV release in seconds instead
// of lingering in Terminating for the full TTLSecondsAfterFinished window
// (600s for task Jobs, 3600s for runner Jobs).
//
// S3 data is untouched — only Kubernetes objects are removed here.
// AFTER:
func (r *RunReconciler) cleanupWorkspace(ctx context.Context, run *domain.Run, terminalState domain.State) {
	if r.workspace == nil {
		return
	}
	if run.Workspace == nil || !run.Workspace.RequiresCleanup() {
		return
	}

	r.log.Info("cleaning up workspace",
		"run_id", run.RunID,
		"kind", run.Workspace.Kind,
		"claim_name", run.Workspace.ClaimName,
		"pv_name", run.Workspace.PVName,
	)

	// Only delete Jobs eagerly on clean success. For failed/canceled runs,
	// pod logs are needed for debugging — let TTLSecondsAfterFinished handle
	// Job cleanup instead. The PVC will stay in Terminating until TTL expires
	// (up to 3600s for runner jobs), which is acceptable.
	if terminalState == domain.StateComplete {
		if err := r.backend.DeleteRunJobs(ctx, run.RunID); err != nil {
			r.log.Error("failed to delete run jobs before workspace cleanup",
				"run_id", run.RunID,
				"err", err,
			)
		}
	}

	if err := r.workspace.Cleanup(ctx, run.Workspace); err != nil {
		r.log.Error("failed to cleanup workspace",
			"run_id", run.RunID,
			"kind", run.Workspace.Kind,
			"claim_name", run.Workspace.ClaimName,
			"pv_name", run.Workspace.PVName,
			"err", err,
		)
	}
}
