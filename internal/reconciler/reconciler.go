// Package reconciler implements the background reconciliation loop that
// polls active tasks, refreshes their state from the backend, and updates
// the database accordingly.
//
// The reconciler is the single source of truth for task state transitions
// driven by the backend. The request path (TES handler) only sets the
// initial state and submits; everything else flows through here.
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

// Config holds reconciler configuration.
type Config struct {
	PollInterval time.Duration // how often to poll (default 5s)
	BatchSize    int           // max tasks per poll cycle (default 100)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval: 5 * time.Second,
		BatchSize:    100,
	}
}

// Reconciler periodically polls active tasks and syncs their state.
type Reconciler struct {
	tasks   repository.TaskStore
	events  repository.EventStore
	backend backend.TaskBackend
	cfg     Config
	log     *slog.Logger
}

// New creates a Reconciler.
func New(
	tasks repository.TaskStore,
	events repository.EventStore,
	be backend.TaskBackend,
	cfg Config,
	log *slog.Logger,
) *Reconciler {
	return &Reconciler{
		tasks:   tasks,
		events:  events,
		backend: be,
		cfg:     cfg,
		log:     log,
	}
}

// Run starts the reconciliation loop. It blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	r.log.Info("reconciler started",
		"poll_interval", r.cfg.PollInterval,
		"batch_size", r.cfg.BatchSize,
	)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce runs a single reconciliation pass.
func (r *Reconciler) reconcileOnce(ctx context.Context) {
	tasks, err := r.tasks.ListActiveTasks(ctx, r.cfg.BatchSize)
	if err != nil {
		r.log.Error("failed to list active tasks", "err", err)
		return
	}

	if len(tasks) == 0 {
		return
	}

	r.log.Debug("reconciling active tasks", "count", len(tasks))

	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		r.reconcileTask(ctx, task)
	}
}

// reconcileTask syncs a single task's state from the backend.
func (r *Reconciler) reconcileTask(ctx context.Context, task *domain.Task) {
	status, err := r.backend.Refresh(ctx, task)
	if err != nil {
		r.log.Error("failed to refresh task",
			"task_id", task.TESTaskID,
			"err", err,
		)
		return
	}

	// No state change needed.
	if status.Phase == task.State {
		return
	}

	// Validate the transition.
	if err := domain.ValidateTaskTransition(task.State, status.Phase); err != nil {
		r.log.Warn("invalid state transition from backend",
			"task_id", task.TESTaskID,
			"from", task.State,
			"to", status.Phase,
			"reason", status.Reason,
			"err", err,
		)
		return
	}

	// Persist the exit code before publishing a terminal state. The nf-ga4gh
	// executor polls the task state first and then immediately requests the FULL
	// TES view to read logs[0].logs[0].exitCode. If we flip the state to a
	// terminal value before exit_code is stored, Nextflow can observe COMPLETE
	// with a missing exit code and treat the task as externally aborted.
	if status.ExitCode != nil {
		if err := r.tasks.SetExitCode(ctx, task.TaskID, *status.ExitCode); err != nil {
			r.log.Error("failed to set exit code",
				"task_id", task.TESTaskID,
				"exit_code", *status.ExitCode,
				"err", err,
			)
			return
		}
	}

	// Update state in the database.
	if err := r.tasks.UpdateState(ctx, task.TaskID, task.State, status.Phase); err != nil {
		r.log.Error("failed to update task state",
			"task_id", task.TESTaskID,
			"from", task.State,
			"to", status.Phase,
			"err", err,
		)
		return
	}

	r.log.Info("task state updated",
		"task_id", task.TESTaskID,
		"from", task.State,
		"to", status.Phase,
		"reason", status.Reason,
		"pod", status.PodName,
	)

	// Update backend state blob.
	if status.PodName != "" {
		bs, _ := json.Marshal(map[string]any{
			"pod_name": status.PodName,
			"reason":   status.Reason,
		})
		if err := r.tasks.SetBackendState(ctx, task.TaskID, bs); err != nil {
			r.log.Error("failed to persist task backend state",
				"task_id", task.TESTaskID,
				"err", err,
			)
		}
	}

	// Append event.
	payload, _ := json.Marshal(domain.StateChangePayload{
		FromState: task.State,
		ToState:   status.Phase,
		Reason:    status.Reason,
	})
	event := domain.NewEvent(task.RunID, task.TaskID, domain.EventTaskStateChanged, payload)
	if err := r.events.Append(ctx, event); err != nil {
		r.log.Error("failed to append state change event",
			"task_id", task.TESTaskID,
			"err", err,
		)
	}
}
