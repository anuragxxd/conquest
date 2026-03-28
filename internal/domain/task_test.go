package domain

import (
	"errors"
	"testing"
)

func TestNewTask(t *testing.T) {
	task := NewTask("run-abc")
	if task.TaskID == "" {
		t.Fatal("TaskID should be set")
	}
	if task.RunID != "run-abc" {
		t.Errorf("RunID = %s, want run-abc", task.RunID)
	}
	if task.State != StateQueued {
		t.Errorf("initial state = %s, want QUEUED", task.State)
	}
	if task.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if task.StartedAt != nil {
		t.Error("StartedAt should be nil initially")
	}
	if task.FinishedAt != nil {
		t.Error("FinishedAt should be nil initially")
	}
	if task.Attempt != 0 {
		t.Errorf("Attempt = %d, want 0", task.Attempt)
	}
	if task.ExitCode != nil {
		t.Error("ExitCode should be nil initially")
	}
}

func TestTask_HappyPath(t *testing.T) {
	task := NewTask("run-abc")

	if err := task.Transition(StateInitializing); err != nil {
		t.Fatalf("QUEUED -> INITIALIZING: %v", err)
	}
	if task.StartedAt == nil {
		t.Error("StartedAt should be set after INITIALIZING")
	}

	if err := task.Transition(StateRunning); err != nil {
		t.Fatalf("INITIALIZING -> RUNNING: %v", err)
	}

	if err := task.Transition(StateComplete); err != nil {
		t.Fatalf("RUNNING -> COMPLETE: %v", err)
	}
	if task.FinishedAt == nil {
		t.Error("FinishedAt should be set after COMPLETE")
	}
}

func TestTask_DirectCancel(t *testing.T) {
	// Tasks can go directly to CANCELED from any active non-terminal state.
	for _, from := range []State{StateQueued, StateInitializing, StateRunning} {
		task := NewTask("run-cancel")
		switch from {
		case StateInitializing:
			_ = task.Transition(StateInitializing)
		case StateRunning:
			_ = task.Transition(StateInitializing)
			_ = task.Transition(StateRunning)
		}
		if err := task.Transition(StateCanceled); err != nil {
			t.Errorf("%s -> CANCELED should be valid, got: %v", from, err)
		}
		if task.FinishedAt == nil {
			t.Errorf("FinishedAt should be set after CANCELED (from %s)", from)
		}
	}
}

func TestTask_NoCanceling(t *testing.T) {
	// Tasks must not use CANCELING — nf-ga4gh would hang.
	task := NewTask("run-x")
	_ = task.Transition(StateInitializing)
	_ = task.Transition(StateRunning)

	err := task.Transition(StateCanceling)
	if err == nil {
		t.Fatal("RUNNING -> CANCELING should be blocked for tasks")
	}
}

func TestTask_TerminalBlocked(t *testing.T) {
	task := NewTask("run-x")
	_ = task.Transition(StateInitializing)
	_ = task.Transition(StateRunning)
	_ = task.Transition(StateComplete)

	err := task.Transition(StateRunning)
	if err == nil {
		t.Fatal("COMPLETE -> RUNNING should be blocked")
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TransitionError, got %T", err)
	}
}

func TestTask_SameStateNoOp(t *testing.T) {
	task := NewTask("run-x")
	before := task.UpdatedAt
	if err := task.Transition(StateQueued); err != nil {
		t.Fatalf("same-state: %v", err)
	}
	if task.UpdatedAt != before {
		t.Error("UpdatedAt should not change on same-state no-op")
	}
}

func TestTask_SetExitCode(t *testing.T) {
	task := NewTask("run-x")
	task.SetExitCode(0)
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", task.ExitCode)
	}
	task.SetExitCode(137)
	if *task.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", *task.ExitCode)
	}
}

func TestTask_SetError(t *testing.T) {
	task := NewTask("run-x")
	_ = task.Transition(StateInitializing)
	_ = task.Transition(StateRunning)

	code := int32(1)
	err := task.SetError(StateExecutorError, &code)
	if err != nil {
		t.Fatalf("SetError: %v", err)
	}
	if task.State != StateExecutorError {
		t.Errorf("state = %s, want EXECUTOR_ERROR", task.State)
	}
	if task.ExitCode == nil || *task.ExitCode != 1 {
		t.Errorf("ExitCode = %v, want 1", task.ExitCode)
	}
}

func TestTask_SetError_NilExitCode(t *testing.T) {
	task := NewTask("run-x")
	_ = task.Transition(StateInitializing)

	err := task.SetError(StateSystemError, nil)
	if err != nil {
		t.Fatalf("SetError: %v", err)
	}
	if task.ExitCode != nil {
		t.Error("ExitCode should remain nil when not provided")
	}
}

func TestTask_SetError_RejectsNonError(t *testing.T) {
	task := NewTask("run-x")
	err := task.SetError(StateRunning, nil)
	if err == nil {
		t.Fatal("SetError with non-error state should fail")
	}
}

func TestTask_SystemErrorFromAnyActive(t *testing.T) {
	for _, from := range []State{StateQueued, StateInitializing, StateRunning} {
		task := NewTask("run-x")
		switch from {
		case StateInitializing:
			_ = task.Transition(StateInitializing)
		case StateRunning:
			_ = task.Transition(StateInitializing)
			_ = task.Transition(StateRunning)
		}
		if err := task.Transition(StateSystemError); err != nil {
			t.Errorf("task %s -> SYSTEM_ERROR should be valid, got: %v", from, err)
		}
	}
}

func TestTask_UniqueIDs(t *testing.T) {
	ids := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		task := NewTask("run-x")
		if ids[task.TaskID] {
			t.Fatalf("duplicate TaskID: %s", task.TaskID)
		}
		ids[task.TaskID] = true
	}
}
