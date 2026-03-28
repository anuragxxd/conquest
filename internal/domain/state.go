// Package domain defines the core domain types for the Conquest workflow
// execution platform, including the unified GA4GH state machine shared by
// both WES (runs) and TES (tasks).
package domain

import (
	"fmt"
)

// State represents a GA4GH v1.1 execution state.
//
// Both WES and TES define the same 11-value enum. We use a single type so
// the domain layer, database, and API serialisation all speak the same
// vocabulary. The valid *transitions* differ between runs and tasks — see
// RunTransitions and TaskTransitions.
type State string

const (
	StateUnknown       State = "UNKNOWN"
	StateQueued        State = "QUEUED"
	StateInitializing  State = "INITIALIZING"
	StateRunning       State = "RUNNING"
	StatePaused        State = "PAUSED"
	StateComplete      State = "COMPLETE"
	StateExecutorError State = "EXECUTOR_ERROR"
	StateSystemError   State = "SYSTEM_ERROR"
	StateCanceled      State = "CANCELED"
	StateCanceling     State = "CANCELING"
	StatePreempted     State = "PREEMPTED"
)

// AllStates contains every state in the GA4GH v1.1 enum.
var AllStates = []State{
	StateUnknown,
	StateQueued,
	StateInitializing,
	StateRunning,
	StatePaused,
	StateComplete,
	StateExecutorError,
	StateSystemError,
	StateCanceled,
	StateCanceling,
	StatePreempted,
}

// TerminalStates are states from which no further transitions are possible.
var TerminalStates = newStateSet(
	StateComplete,
	StateExecutorError,
	StateSystemError,
	StateCanceled,
	StatePreempted,
)

// ActiveStates are non-terminal states that indicate work is in progress.
var ActiveStates = newStateSet(
	StateInitializing,
	StateRunning,
	StateCanceling,
)

// PendingWorkStates are states that need the controller/reconciler to act.
// This includes everything that is not terminal and not UNKNOWN.
var PendingWorkStates = newStateSet(
	StateQueued,
	StateInitializing,
	StateRunning,
	StateCanceling,
)

// ---------------------------------------------------------------------------
// Transition maps
// ---------------------------------------------------------------------------

// transitionMap defines which target states are reachable from a given source.
type transitionMap map[State]StateSet

// RunTransitions defines valid state transitions for WES runs.
//
// Key differences from TaskTransitions:
//   - Runs go through CANCELING before reaching CANCELED (the platform
//     requests cancellation of individual tasks, then marks the run
//     canceled once all are done).
//   - Any non-terminal state can reach SYSTEM_ERROR (infra failure).
//   - QUEUED and INITIALIZING can reach CANCELED directly if cancellation
//     is requested before execution starts.
var RunTransitions = transitionMap{
	StateUnknown: newStateSet(
		StateQueued,
	),
	StateQueued: newStateSet(
		StateInitializing,
		StateCanceling,
		StateSystemError,
	),
	StateInitializing: newStateSet(
		StateComplete,
		StateRunning,
		StateCanceling,
		StateExecutorError,
		StateSystemError,
	),
	StateRunning: newStateSet(
		StateComplete,
		StateCanceling,
		StateExecutorError,
		StateSystemError,
	),
	StateCanceling: newStateSet(
		StateCanceled,
		StateSystemError,
	),
	// Terminal states: no outgoing transitions.
	StateComplete:      newStateSet(),
	StateExecutorError: newStateSet(),
	StateSystemError:   newStateSet(),
	StateCanceled:      newStateSet(),
	// Not used in v1 — no transitions defined.
	StatePaused:    newStateSet(),
	StatePreempted: newStateSet(),
}

// TaskTransitions defines valid state transitions for TES tasks.
//
// Key differences from RunTransitions:
//   - Tasks go directly to CANCELED (the K8s Job is killed).
//   - No CANCELING state is used. The nf-ga4gh plugin does not handle
//     CANCELING, so the TES backend must not emit it for v1.
//   - QUEUED can reach CANCELED directly (task canceled before a pod starts).
var TaskTransitions = transitionMap{
	StateUnknown: newStateSet(
		StateQueued,
	),
	StateQueued: newStateSet(
		StateInitializing,
		StateCanceled,
		StateSystemError,
	),
	StateInitializing: newStateSet(
		StateComplete,
		StateRunning,
		StateCanceled,
		StateExecutorError,
		StateSystemError,
	),
	StateRunning: newStateSet(
		StateComplete,
		StateCanceled,
		StateExecutorError,
		StateSystemError,
	),
	// Terminal states: no outgoing transitions.
	StateComplete:      newStateSet(),
	StateExecutorError: newStateSet(),
	StateSystemError:   newStateSet(),
	StateCanceled:      newStateSet(),
	// Not used in v1.
	StatePaused:    newStateSet(),
	StateCanceling: newStateSet(),
	StatePreempted: newStateSet(),
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidateRunTransition checks whether transitioning a run from `from` to `to`
// is permitted. A same-state transition (from == to) is always allowed as a
// no-op. Returns a *TransitionError on invalid transitions.
func ValidateRunTransition(from, to State) error {
	return validateTransition(RunTransitions, from, to)
}

// ValidateTaskTransition checks whether transitioning a task from `from` to
// `to` is permitted. Same-state is allowed. Returns a *TransitionError on
// invalid transitions.
func ValidateTaskTransition(from, to State) error {
	return validateTransition(TaskTransitions, from, to)
}

func validateTransition(tm transitionMap, from, to State) error {
	if from == to {
		return nil
	}
	allowed, ok := tm[from]
	if !ok {
		return &TransitionError{From: from, To: to, Reason: fmt.Sprintf("unknown source state %q", from)}
	}
	if !allowed.Contains(to) {
		return &TransitionError{From: from, To: to}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

// IsTerminal reports whether s is a terminal (sink) state.
func (s State) IsTerminal() bool { return TerminalStates.Contains(s) }

// IsActive reports whether s indicates work is currently in progress.
func (s State) IsActive() bool { return ActiveStates.Contains(s) }

// NeedsPlatformWork reports whether s requires the controller/reconciler to
// take action (scheduling, polling, canceling, etc.).
func (s State) NeedsPlatformWork() bool { return PendingWorkStates.Contains(s) }

// IsValid reports whether s is one of the 11 GA4GH states.
func (s State) IsValid() bool {
	for _, v := range AllStates {
		if s == v {
			return true
		}
	}
	return false
}

// IsError reports whether s represents a failure.
func (s State) IsError() bool {
	return s == StateExecutorError || s == StateSystemError
}

// ---------------------------------------------------------------------------
// TransitionError
// ---------------------------------------------------------------------------

// TransitionError is returned when a state transition is not permitted.
type TransitionError struct {
	From   State
	To     State
	Reason string // optional extra context
}

func (e *TransitionError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("invalid state transition %s -> %s: %s", e.From, e.To, e.Reason)
	}
	return fmt.Sprintf("invalid state transition %s -> %s", e.From, e.To)
}

// ---------------------------------------------------------------------------
// StateSet — a small helper for readable state membership checks.
// ---------------------------------------------------------------------------

// StateSet is an immutable set of states.
type StateSet struct {
	m map[State]struct{}
}

func newStateSet(states ...State) StateSet {
	m := make(map[State]struct{}, len(states))
	for _, s := range states {
		m[s] = struct{}{}
	}
	return StateSet{m: m}
}

// Contains reports whether s is in the set.
func (ss StateSet) Contains(s State) bool {
	_, ok := ss.m[s]
	return ok
}

// Slice returns the states as a slice (order is not guaranteed).
func (ss StateSet) Slice() []State {
	out := make([]State, 0, len(ss.m))
	for s := range ss.m {
		out = append(out, s)
	}
	return out
}

// Len returns the number of states in the set.
func (ss StateSet) Len() int { return len(ss.m) }
