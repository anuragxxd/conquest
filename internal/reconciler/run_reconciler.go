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

// RunReconciler periodically polls active runs and syncs their state
// from the runner backend (K8s Jobs).
type RunReconciler struct {
	runs    repository.RunStore
	events  repository.EventStore
	backend backend.RunBackend
	cfg     Config
	log     *slog.Logger
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
}
