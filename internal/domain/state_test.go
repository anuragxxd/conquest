package domain

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Run transition tests
// ---------------------------------------------------------------------------

func TestValidateRunTransition_SameState(t *testing.T) {
	for _, s := range AllStates {
		if err := ValidateRunTransition(s, s); err != nil {
			t.Errorf("same-state transition %s -> %s should be allowed, got: %v", s, s, err)
		}
	}
}

func TestValidateRunTransition_HappyPath(t *testing.T) {
	// The normal successful lifecycle for a run.
	happy := []struct{ from, to State }{
		{StateUnknown, StateQueued},
		{StateQueued, StateInitializing},
		{StateInitializing, StateComplete},
		{StateInitializing, StateRunning},
		{StateRunning, StateComplete},
	}
	for _, tc := range happy {
		if err := ValidateRunTransition(tc.from, tc.to); err != nil {
			t.Errorf("run %s -> %s should be valid, got: %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateRunTransition_CancelPath(t *testing.T) {
	// Cancellation goes through CANCELING for runs.
	cancel := []struct{ from, to State }{
		{StateQueued, StateCanceling},
		{StateInitializing, StateCanceling},
		{StateRunning, StateCanceling},
		{StateCanceling, StateCanceled},
	}
	for _, tc := range cancel {
		if err := ValidateRunTransition(tc.from, tc.to); err != nil {
			t.Errorf("run cancel %s -> %s should be valid, got: %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateRunTransition_ErrorPaths(t *testing.T) {
	// SYSTEM_ERROR reachable from any non-terminal active state.
	for _, from := range []State{StateQueued, StateInitializing, StateRunning, StateCanceling} {
		if err := ValidateRunTransition(from, StateSystemError); err != nil {
			t.Errorf("run %s -> SYSTEM_ERROR should be valid, got: %v", from, err)
		}
	}
	// EXECUTOR_ERROR reachable from INITIALIZING and RUNNING.
	for _, from := range []State{StateInitializing, StateRunning} {
		if err := ValidateRunTransition(from, StateExecutorError); err != nil {
			t.Errorf("run %s -> EXECUTOR_ERROR should be valid, got: %v", from, err)
		}
	}
}

func TestValidateRunTransition_TerminalBlocked(t *testing.T) {
	terminals := []State{StateComplete, StateExecutorError, StateSystemError, StateCanceled}
	targets := []State{StateQueued, StateInitializing, StateRunning, StateComplete, StateCanceling}
	for _, from := range terminals {
		for _, to := range targets {
			if from == to {
				continue // same-state is always OK
			}
			err := ValidateRunTransition(from, to)
			if err == nil {
				t.Errorf("run %s -> %s should be blocked (terminal source), but was allowed", from, to)
			}
			var te *TransitionError
			if !errors.As(err, &te) {
				t.Errorf("expected *TransitionError, got %T", err)
			}
		}
	}
}

func TestValidateRunTransition_NoDirectCanceledFromQueued(t *testing.T) {
	// Runs must go through CANCELING, not directly to CANCELED.
	err := ValidateRunTransition(StateQueued, StateCanceled)
	if err == nil {
		t.Error("run QUEUED -> CANCELED should be blocked (must go through CANCELING)")
	}
}

func TestValidateRunTransition_PausedNotUsed(t *testing.T) {
	// PAUSED is in the enum but not reachable in v1.
	err := ValidateRunTransition(StateRunning, StatePaused)
	if err == nil {
		t.Error("run RUNNING -> PAUSED should be blocked in v1")
	}
	err = ValidateRunTransition(StatePaused, StateRunning)
	if err == nil {
		t.Error("run PAUSED -> RUNNING should be blocked in v1")
	}
}

func TestValidateRunTransition_PreemptedNotUsed(t *testing.T) {
	err := ValidateRunTransition(StateRunning, StatePreempted)
	if err == nil {
		t.Error("run RUNNING -> PREEMPTED should be blocked in v1")
	}
}

// Exhaustive: every (from, to) pair where from != to that is NOT in the
// transition map must return an error.
func TestValidateRunTransition_Exhaustive(t *testing.T) {
	for _, from := range AllStates {
		allowed := RunTransitions[from]
		for _, to := range AllStates {
			if from == to {
				continue
			}
			err := ValidateRunTransition(from, to)
			if allowed.Contains(to) {
				if err != nil {
					t.Errorf("run %s -> %s should be allowed, got: %v", from, to, err)
				}
			} else {
				if err == nil {
					t.Errorf("run %s -> %s should be blocked, but was allowed", from, to)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Task transition tests
// ---------------------------------------------------------------------------

func TestValidateTaskTransition_SameState(t *testing.T) {
	for _, s := range AllStates {
		if err := ValidateTaskTransition(s, s); err != nil {
			t.Errorf("same-state transition %s -> %s should be allowed, got: %v", s, s, err)
		}
	}
}

func TestValidateTaskTransition_HappyPath(t *testing.T) {
	happy := []struct{ from, to State }{
		{StateUnknown, StateQueued},
		{StateQueued, StateInitializing},
		{StateInitializing, StateComplete},
		{StateInitializing, StateRunning},
		{StateRunning, StateComplete},
	}
	for _, tc := range happy {
		if err := ValidateTaskTransition(tc.from, tc.to); err != nil {
			t.Errorf("task %s -> %s should be valid, got: %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateTaskTransition_DirectCancel(t *testing.T) {
	// Tasks go directly to CANCELED (no CANCELING step).
	for _, from := range []State{StateQueued, StateInitializing, StateRunning} {
		if err := ValidateTaskTransition(from, StateCanceled); err != nil {
			t.Errorf("task %s -> CANCELED should be valid, got: %v", from, err)
		}
	}
}

func TestValidateTaskTransition_NoCanceling(t *testing.T) {
	// Tasks must NOT use CANCELING in v1 (nf-ga4gh doesn't handle it).
	for _, from := range []State{StateQueued, StateInitializing, StateRunning} {
		err := ValidateTaskTransition(from, StateCanceling)
		if err == nil {
			t.Errorf("task %s -> CANCELING should be blocked in v1", from)
		}
	}
}

func TestValidateTaskTransition_ErrorPaths(t *testing.T) {
	for _, from := range []State{StateQueued, StateInitializing, StateRunning} {
		if err := ValidateTaskTransition(from, StateSystemError); err != nil {
			t.Errorf("task %s -> SYSTEM_ERROR should be valid, got: %v", from, err)
		}
	}
	for _, from := range []State{StateInitializing, StateRunning} {
		if err := ValidateTaskTransition(from, StateExecutorError); err != nil {
			t.Errorf("task %s -> EXECUTOR_ERROR should be valid, got: %v", from, err)
		}
	}
}

func TestValidateTaskTransition_TerminalBlocked(t *testing.T) {
	terminals := []State{StateComplete, StateExecutorError, StateSystemError, StateCanceled}
	for _, from := range terminals {
		for _, to := range AllStates {
			if from == to {
				continue
			}
			err := ValidateTaskTransition(from, to)
			if err == nil {
				t.Errorf("task %s -> %s should be blocked (terminal source), but was allowed", from, to)
			}
		}
	}
}

func TestValidateTaskTransition_Exhaustive(t *testing.T) {
	for _, from := range AllStates {
		allowed := TaskTransitions[from]
		for _, to := range AllStates {
			if from == to {
				continue
			}
			err := ValidateTaskTransition(from, to)
			if allowed.Contains(to) {
				if err != nil {
					t.Errorf("task %s -> %s should be allowed, got: %v", from, to, err)
				}
			} else {
				if err == nil {
					t.Errorf("task %s -> %s should be blocked, but was allowed", from, to)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// State predicate tests
// ---------------------------------------------------------------------------

func TestState_IsTerminal(t *testing.T) {
	terminal := map[State]bool{
		StateComplete:      true,
		StateExecutorError: true,
		StateSystemError:   true,
		StateCanceled:      true,
		StatePreempted:     true,
	}
	for _, s := range AllStates {
		got := s.IsTerminal()
		want := terminal[s]
		if got != want {
			t.Errorf("%s.IsTerminal() = %v, want %v", s, got, want)
		}
	}
}

func TestState_IsActive(t *testing.T) {
	active := map[State]bool{
		StateInitializing: true,
		StateRunning:      true,
		StateCanceling:    true,
	}
	for _, s := range AllStates {
		got := s.IsActive()
		want := active[s]
		if got != want {
			t.Errorf("%s.IsActive() = %v, want %v", s, got, want)
		}
	}
}

func TestState_NeedsPlatformWork(t *testing.T) {
	needs := map[State]bool{
		StateQueued:       true,
		StateInitializing: true,
		StateRunning:      true,
		StateCanceling:    true,
	}
	for _, s := range AllStates {
		got := s.NeedsPlatformWork()
		want := needs[s]
		if got != want {
			t.Errorf("%s.NeedsPlatformWork() = %v, want %v", s, got, want)
		}
	}
}

func TestState_IsValid(t *testing.T) {
	for _, s := range AllStates {
		if !s.IsValid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if State("BOGUS").IsValid() {
		t.Error("BOGUS should not be valid")
	}
	if State("").IsValid() {
		t.Error("empty string should not be valid")
	}
}

func TestState_IsError(t *testing.T) {
	errs := map[State]bool{
		StateExecutorError: true,
		StateSystemError:   true,
	}
	for _, s := range AllStates {
		got := s.IsError()
		want := errs[s]
		if got != want {
			t.Errorf("%s.IsError() = %v, want %v", s, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TransitionError tests
// ---------------------------------------------------------------------------

func TestTransitionError_Message(t *testing.T) {
	e := &TransitionError{From: StateComplete, To: StateRunning}
	want := "invalid state transition COMPLETE -> RUNNING"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}

	e2 := &TransitionError{From: StateComplete, To: StateRunning, Reason: "terminal state"}
	want2 := "invalid state transition COMPLETE -> RUNNING: terminal state"
	if e2.Error() != want2 {
		t.Errorf("got %q, want %q", e2.Error(), want2)
	}
}

func TestTransitionError_AsError(t *testing.T) {
	err := ValidateRunTransition(StateComplete, StateRunning)
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TransitionError, got %T", err)
	}
	if te.From != StateComplete || te.To != StateRunning {
		t.Errorf("wrong states in error: from=%s to=%s", te.From, te.To)
	}
}

// ---------------------------------------------------------------------------
// StateSet tests
// ---------------------------------------------------------------------------

func TestStateSet_Contains(t *testing.T) {
	ss := newStateSet(StateRunning, StateComplete)
	if !ss.Contains(StateRunning) {
		t.Error("should contain RUNNING")
	}
	if !ss.Contains(StateComplete) {
		t.Error("should contain COMPLETE")
	}
	if ss.Contains(StateQueued) {
		t.Error("should not contain QUEUED")
	}
}

func TestStateSet_Len(t *testing.T) {
	ss := newStateSet(StateRunning, StateComplete, StateQueued)
	if ss.Len() != 3 {
		t.Errorf("Len() = %d, want 3", ss.Len())
	}
	empty := newStateSet()
	if empty.Len() != 0 {
		t.Errorf("empty Len() = %d, want 0", empty.Len())
	}
}

func TestStateSet_Slice(t *testing.T) {
	ss := newStateSet(StateRunning, StateComplete)
	slice := ss.Slice()
	if len(slice) != 2 {
		t.Fatalf("Slice() len = %d, want 2", len(slice))
	}
	found := map[State]bool{}
	for _, s := range slice {
		found[s] = true
	}
	if !found[StateRunning] || !found[StateComplete] {
		t.Errorf("Slice() = %v, expected RUNNING and COMPLETE", slice)
	}
}
