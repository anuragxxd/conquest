package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func newTestRun() *Run {
	return NewRun(
		"NFL", "DSL2",
		"nextflow", "24.10.0",
		json.RawMessage(`{"workflow_url":"https://example.com/wf.nf"}`),
		"s3://bucket/v1/workflow-runs/test/outputs/",
		"v1",
		"s3://bucket/v1/workflow-runs/test/",
	)
}

func TestNewRun(t *testing.T) {
	r := newTestRun()
	if r.RunID == "" {
		t.Fatal("RunID should be set")
	}
	if r.State != StateQueued {
		t.Errorf("initial state = %s, want QUEUED", r.State)
	}
	if r.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if r.StartedAt != nil {
		t.Error("StartedAt should be nil initially")
	}
	if r.FinishedAt != nil {
		t.Error("FinishedAt should be nil initially")
	}
	if r.LayoutVersion != "v1" {
		t.Errorf("LayoutVersion = %s, want v1", r.LayoutVersion)
	}
}

func TestRun_HappyPath(t *testing.T) {
	r := newTestRun()

	// QUEUED -> INITIALIZING
	if err := r.Transition(StateInitializing); err != nil {
		t.Fatalf("QUEUED -> INITIALIZING: %v", err)
	}
	if r.State != StateInitializing {
		t.Errorf("state = %s, want INITIALIZING", r.State)
	}
	if r.StartedAt == nil {
		t.Error("StartedAt should be set after INITIALIZING")
	}
	startedAt := *r.StartedAt

	// INITIALIZING -> RUNNING
	if err := r.Transition(StateRunning); err != nil {
		t.Fatalf("INITIALIZING -> RUNNING: %v", err)
	}
	if r.StartedAt == nil || *r.StartedAt != startedAt {
		t.Error("StartedAt should not change after first set")
	}

	// RUNNING -> COMPLETE
	if err := r.Transition(StateComplete); err != nil {
		t.Fatalf("RUNNING -> COMPLETE: %v", err)
	}
	if r.FinishedAt == nil {
		t.Error("FinishedAt should be set after terminal state")
	}
}

func TestRun_CancelPath(t *testing.T) {
	r := newTestRun()
	_ = r.Transition(StateInitializing)
	_ = r.Transition(StateRunning)

	if err := r.Transition(StateCanceling); err != nil {
		t.Fatalf("RUNNING -> CANCELING: %v", err)
	}
	if r.FinishedAt != nil {
		t.Error("FinishedAt should not be set for CANCELING (non-terminal)")
	}

	if err := r.Transition(StateCanceled); err != nil {
		t.Fatalf("CANCELING -> CANCELED: %v", err)
	}
	if r.FinishedAt == nil {
		t.Error("FinishedAt should be set after CANCELED")
	}
}

func TestRun_SystemErrorFromAnyActive(t *testing.T) {
	states := []State{StateQueued, StateInitializing, StateRunning, StateCanceling}
	for _, from := range states {
		r := newTestRun()
		// Walk to the desired state.
		switch from {
		case StateInitializing:
			_ = r.Transition(StateInitializing)
		case StateRunning:
			_ = r.Transition(StateInitializing)
			_ = r.Transition(StateRunning)
		case StateCanceling:
			_ = r.Transition(StateInitializing)
			_ = r.Transition(StateRunning)
			_ = r.Transition(StateCanceling)
		}
		if err := r.Transition(StateSystemError); err != nil {
			t.Errorf("%s -> SYSTEM_ERROR should be valid, got: %v", from, err)
		}
	}
}

func TestRun_TerminalBlocked(t *testing.T) {
	r := newTestRun()
	_ = r.Transition(StateInitializing)
	_ = r.Transition(StateRunning)
	_ = r.Transition(StateComplete)

	err := r.Transition(StateRunning)
	if err == nil {
		t.Fatal("COMPLETE -> RUNNING should be blocked")
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TransitionError, got %T", err)
	}
}

func TestRun_SameStateNoOp(t *testing.T) {
	r := newTestRun()
	before := r.UpdatedAt
	if err := r.Transition(StateQueued); err != nil {
		t.Fatalf("same-state should not error: %v", err)
	}
	if r.UpdatedAt != before {
		t.Error("UpdatedAt should not change on same-state no-op")
	}
}

func TestRun_SetError(t *testing.T) {
	r := newTestRun()
	_ = r.Transition(StateInitializing)

	err := r.SetError(StateExecutorError, "non-zero exit code")
	if err != nil {
		t.Fatalf("SetError: %v", err)
	}
	if r.State != StateExecutorError {
		t.Errorf("state = %s, want EXECUTOR_ERROR", r.State)
	}
	if r.ErrorSummary != "non-zero exit code" {
		t.Errorf("ErrorSummary = %q, want %q", r.ErrorSummary, "non-zero exit code")
	}
}

func TestRun_SetError_RejectsNonError(t *testing.T) {
	r := newTestRun()
	err := r.SetError(StateRunning, "oops")
	if err == nil {
		t.Fatal("SetError with non-error state should fail")
	}
}

func TestRun_UniqueIDs(t *testing.T) {
	ids := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		r := newTestRun()
		if ids[r.RunID] {
			t.Fatalf("duplicate RunID: %s", r.RunID)
		}
		ids[r.RunID] = true
	}
}
