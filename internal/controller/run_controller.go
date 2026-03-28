// Package controller implements the business logic orchestration layer.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anuragxxd/conquest/internal/adapter"
	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
	"github.com/anuragxxd/conquest/internal/storage"
)

// ---------------------------------------------------------------------------
// Controller-level types (no dependency on api/wes or api/tes)
// ---------------------------------------------------------------------------

// RunRequest is the controller's view of a parsed WES submission.
type RunRequest struct {
	WorkflowType             string            `json:"workflow_type"`
	WorkflowTypeVersion      string            `json:"workflow_type_version"`
	WorkflowURL              string            `json:"workflow_url"`
	WorkflowParams           json.RawMessage   `json:"workflow_params,omitempty"`
	WorkflowEngine           string            `json:"workflow_engine,omitempty"`
	WorkflowEngineVersion    string            `json:"workflow_engine_version,omitempty"`
	WorkflowEngineParameters json.RawMessage   `json:"workflow_engine_parameters,omitempty"`
	Tags                     map[string]string `json:"tags,omitempty"`
}

// RunLog is the controller's assembled run response.
type RunLog struct {
	Run         *domain.Run
	TaskLogsURL string
}

// RunListResult holds a page of runs.
type RunListResult struct {
	Runs      []*domain.Run
	NextToken string
}

// TaskListResult holds a page of tasks.
type TaskListResult struct {
	Tasks     []*domain.Task
	NextToken string
}

// StateCounts maps states to counts.
type StateCounts map[domain.State]int

// ---------------------------------------------------------------------------
// RunController
// ---------------------------------------------------------------------------

// RunController handles WES run lifecycle operations.
type RunController struct {
	runs        repository.RunStore
	tasks       repository.TaskStore
	events      repository.EventStore
	layout      storage.Layout
	adapters    *adapter.Registry
	runBackend  backend.RunBackend  // nil if no runner backend configured
	taskBackend backend.TaskBackend // nil if no task backend configured
	log         *slog.Logger
}

// NewRunController creates a RunController.
func NewRunController(
	runs repository.RunStore,
	tasks repository.TaskStore,
	events repository.EventStore,
	layout storage.Layout,
	adapters *adapter.Registry,
	log *slog.Logger,
) *RunController {
	return &RunController{
		runs:     runs,
		tasks:    tasks,
		events:   events,
		layout:   layout,
		adapters: adapters,
		log:      log,
	}
}

// SetRunBackend configures the run-level execution backend.
func (c *RunController) SetRunBackend(be backend.RunBackend) {
	c.runBackend = be
}

// SetTaskBackend configures the task-level execution backend.
func (c *RunController) SetTaskBackend(be backend.TaskBackend) {
	c.taskBackend = be
}

// Layout returns the storage layout used by this controller.
func (c *RunController) Layout() storage.Layout {
	return c.layout
}

func (c *RunController) SupportedEngines() []string {
	if c.adapters == nil {
		return nil
	}
	return c.adapters.Names()
}

// SubmitRun validates and persists a new workflow run.
func (c *RunController) SubmitRun(ctx context.Context, req *RunRequest) (*domain.Run, error) {
	if req.WorkflowType == "" {
		return nil, &adapter.ValidationError{Field: "workflow_type", Msg: "required"}
	}
	if req.WorkflowTypeVersion == "" {
		return nil, &adapter.ValidationError{Field: "workflow_type_version", Msg: "required"}
	}
	if req.WorkflowURL == "" {
		return nil, &adapter.ValidationError{Field: "workflow_url", Msg: "required"}
	}
	if c.adapters == nil {
		return nil, fmt.Errorf("no engine adapters configured")
	}

	engine := adapter.InferEngine(req.WorkflowType, req.WorkflowEngine)
	runAdapter, ok := c.adapters.Get(engine)
	if !ok {
		return nil, &adapter.ValidationError{Field: "workflow_engine", Msg: fmt.Sprintf("unsupported engine %q", engine)}
	}

	runID := domain.NewID()
	prepared, err := runAdapter.PrepareRun(runID, adapter.SubmissionRequest{
		WorkflowType:             req.WorkflowType,
		WorkflowTypeVersion:      req.WorkflowTypeVersion,
		WorkflowURL:              req.WorkflowURL,
		WorkflowParams:           req.WorkflowParams,
		WorkflowEngine:           engine,
		WorkflowEngineVersion:    req.WorkflowEngineVersion,
		WorkflowEngineParameters: req.WorkflowEngineParameters,
		Tags:                     req.Tags,
	}, c.layout)
	if err != nil {
		return nil, err
	}

	run := domain.NewRunWithID(
		runID,
		req.WorkflowType,
		req.WorkflowTypeVersion,
		prepared.WorkflowEngine,
		prepared.WorkflowEngineVersion,
		prepared.RequestPayload,
		"", // set below
		c.layout.CurrentVersion(),
		"", // set below
	)
	run.OutputPrefix = c.layout.OutputsDirURI(run.RunID)
	run.RunRootURI = c.layout.RunRootURI(run.RunID)
	run.ResolvedWorkflowRef = prepared.ResolvedWorkflowRef

	if err := c.runs.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	event := domain.NewEvent(run.RunID, "", domain.EventRunCreated, prepared.RequestPayload)
	if err := c.events.Append(ctx, event); err != nil {
		c.log.Error("failed to append run.created event", "run_id", run.RunID, "err", err)
	}

	// Launch the runner Job if a run backend is configured.
	if c.runBackend != nil {
		submission, err := c.runBackend.SubmitRun(ctx, run, &prepared.RunnerSpec)
		if err != nil {
			// Run was persisted but submission failed. Mark as SYSTEM_ERROR.
			if setErr := c.runs.SetError(ctx, run.RunID, domain.StateSystemError, "runner submission failed: "+err.Error()); setErr != nil {
				c.log.Error("failed to persist run submission error", "run_id", run.RunID, "err", setErr)
			}
			run.ErrorSummary = "runner submission failed: " + err.Error()
			run.State = domain.StateSystemError
			c.log.Error("runner submission failed", "run_id", run.RunID, "err", err)
			return run, nil // return the run even on submission failure
		}
		if submission == nil || submission.BackendRef == "" {
			if setErr := c.runs.SetError(ctx, run.RunID, domain.StateSystemError, "runner submission returned no backend reference"); setErr != nil {
				c.log.Error("failed to persist missing backend ref error", "run_id", run.RunID, "err", setErr)
			}
			run.ErrorSummary = "runner submission returned no backend reference"
			run.State = domain.StateSystemError
			return run, nil
		}
		if err := c.persistSubmittedRun(ctx, run, submission.BackendRef); err != nil {
			c.log.Error("failed to persist submitted run state", "run_id", run.RunID, "backend_ref", submission.BackendRef, "err", err)
			if cancelErr := c.runBackend.CancelRun(context.Background(), &domain.Run{RunID: run.RunID, BackendRef: submission.BackendRef}); cancelErr != nil {
				c.log.Error("failed to clean up runner after persistence error", "run_id", run.RunID, "backend_ref", submission.BackendRef, "err", cancelErr)
			}
			if setErr := c.runs.SetError(ctx, run.RunID, domain.StateSystemError, "failed to persist runner submission: "+err.Error()); setErr != nil {
				c.log.Error("failed to persist runner submission persistence error", "run_id", run.RunID, "err", setErr)
			}
			run.BackendRef = submission.BackendRef
			run.ErrorSummary = "failed to persist runner submission: " + err.Error()
			run.State = domain.StateSystemError
			return run, nil
		}
	}

	c.log.Info("run submitted",
		"run_id", run.RunID,
		"engine", prepared.WorkflowEngine,
		"workflow_url", req.WorkflowURL,
		"resolved_ref", prepared.ResolvedWorkflowRef,
		"backend_ref", run.BackendRef,
	)
	return run, nil
}

// GetRun returns the full run and a task-logs URL.
func (c *RunController) GetRun(ctx context.Context, runID string) (*RunLog, error) {
	run, err := c.runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &RunLog{
		Run:         run,
		TaskLogsURL: fmt.Sprintf("/ga4gh/wes/v1/runs/%s/tasks", run.RunID),
	}, nil
}

// GetRunStatus returns the state of a run.
func (c *RunController) GetRunStatus(ctx context.Context, runID string) (domain.State, error) {
	return c.runs.GetStatus(ctx, runID)
}

// ListRuns returns a paginated list of runs.
func (c *RunController) ListRuns(ctx context.Context, pageSize int, pageToken string) (*RunListResult, error) {
	result, err := c.runs.List(ctx, repository.Page{Size: pageSize, Token: pageToken})
	if err != nil {
		return nil, err
	}
	return &RunListResult{Runs: result.Items, NextToken: result.NextToken}, nil
}

// CancelRun transitions the run to CANCELING and stops the runner/tasks.
func (c *RunController) CancelRun(ctx context.Context, runID string) error {
	run, err := c.runs.Get(ctx, runID)
	if err != nil {
		return err
	}

	if err := domain.ValidateRunTransition(run.State, domain.StateCanceling); err != nil {
		return err
	}

	if err := c.runs.UpdateState(ctx, runID, run.State, domain.StateCanceling); err != nil {
		return err
	}

	payload, _ := json.Marshal(domain.StateChangePayload{
		FromState: run.State,
		ToState:   domain.StateCanceling,
		Reason:    "user requested cancellation",
	})
	event := domain.NewEvent(runID, "", domain.EventRunCancelRequested, payload)
	if err := c.events.Append(ctx, event); err != nil {
		c.log.Error("failed to append cancel event", "run_id", runID, "err", err)
	}

	// Cancel the runner Job asynchronously.
	if c.runBackend != nil {
		go func() {
			if err := c.runBackend.CancelRun(context.Background(), run); err != nil {
				c.log.Error("failed to cancel runner", "run_id", runID, "err", err)
			}
		}()
	}

	// Cancel active tasks for this run asynchronously.
	if c.taskBackend != nil {
		go func() {
			tasks, err := c.tasks.ListByRunID(context.Background(), runID, repository.Page{Size: 1000})
			if err != nil {
				c.log.Error("failed to list tasks for cancellation", "run_id", runID, "err", err)
				return
			}
			for _, task := range tasks.Items {
				if task.State.IsTerminal() {
					continue
				}
				if err := c.taskBackend.Cancel(context.Background(), task); err != nil {
					c.log.Error("failed to cancel task backend", "run_id", runID, "task_id", task.TaskID, "err", err)
					continue
				}
				if err := c.tasks.UpdateState(context.Background(), task.TaskID, task.State, domain.StateCanceled); err != nil {
					c.log.Error("failed to mark task canceled", "run_id", runID, "task_id", task.TaskID, "err", err)
				}
			}
		}()
	}

	c.log.Info("run cancel requested", "run_id", runID, "from_state", run.State)
	return nil
}

// ListRunTasks returns paginated tasks for a run.
func (c *RunController) ListRunTasks(ctx context.Context, runID string, pageSize int, pageToken string) (*TaskListResult, error) {
	if _, err := c.runs.GetStatus(ctx, runID); err != nil {
		return nil, err
	}
	result, err := c.tasks.ListByRunID(ctx, runID, repository.Page{Size: pageSize, Token: pageToken})
	if err != nil {
		return nil, err
	}
	return &TaskListResult{Tasks: result.Items, NextToken: result.NextToken}, nil
}

// GetRunTask returns a single task for a run.
func (c *RunController) GetRunTask(ctx context.Context, runID, taskID string) (*domain.Task, error) {
	task, err := c.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.RunID != runID {
		return nil, fmt.Errorf("task %s does not belong to run %s", taskID, runID)
	}
	return task, nil
}

// ServiceStateCounts returns aggregate run counts by state.
func (c *RunController) ServiceStateCounts(ctx context.Context) (StateCounts, error) {
	return c.runs.CountByState(ctx)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// TimeStr formats a *time.Time to RFC3339 or returns empty string.
func TimeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (c *RunController) persistSubmittedRun(ctx context.Context, run *domain.Run, backendRef string) error {
	if err := c.runs.SetBackendRef(ctx, run.RunID, backendRef); err != nil {
		return fmt.Errorf("set backend ref: %w", err)
	}
	if err := c.runs.UpdateState(ctx, run.RunID, domain.StateQueued, domain.StateInitializing); err != nil {
		return fmt.Errorf("set initializing state: %w", err)
	}
	run.BackendRef = backendRef
	run.State = domain.StateInitializing
	return nil
}
