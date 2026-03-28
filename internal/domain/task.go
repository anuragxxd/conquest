package domain

import (
	"encoding/json"
	"time"
)

// Task represents a TES task — a single unit of compute within a Run.
//
// The state machine follows TaskTransitions. Key difference from Run:
// tasks go directly to CANCELED (no CANCELING intermediate), because the
// nf-ga4gh plugin does not handle the CANCELING state.
//
// Timestamp semantics match Run: StartedAt on INITIALIZING, FinishedAt on
// terminal.
type Task struct {
	TaskID              string          `json:"task_id"`
	RunID               string          `json:"run_id"`
	EngineTaskName      string          `json:"engine_task_name,omitempty"` // e.g. Nextflow process name
	TESTaskID           string          `json:"tes_task_id,omitempty"`     // ID as known by the TES API
	State               State           `json:"state"`
	Resources           json.RawMessage `json:"resources,omitempty"`             // TES resources (cpu, ram, disk, etc.)
	TaskRequestPayload  json.RawMessage `json:"task_request_payload,omitempty"`  // Full TES task body as submitted
	BackendRef          string          `json:"backend_ref,omitempty"`           // K8s Job name or other backend ID
	BackendState        json.RawMessage `json:"backend_state,omitempty"`         // Backend-specific state blob (K8s pod phase, etc.)
	OutputLogs          json.RawMessage `json:"output_logs,omitempty"`           // TES OutputFileLog entries from stage-out
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	LogLocation         string          `json:"log_location,omitempty"` // S3 URI to task log
	ExitCode            *int32          `json:"exit_code,omitempty"`    // from TES executor log, not .exitcode file
	Attempt             int32           `json:"attempt"`                // 0-based retry counter
	Workspace           *WorkspaceBinding `json:"workspace,omitempty"`
}

// NewTask creates a Task in QUEUED state with a fresh UUIDv7 ID.
func NewTask(runID string) *Task {
	now := time.Now().UTC()
	return &Task{
		TaskID:    NewID(),
		RunID:     runID,
		State:     StateQueued,
		CreatedAt: now,
		UpdatedAt: now,
		Attempt:   0,
	}
}

// Transition moves the task to a new state, updating timestamps.
// Returns a *TransitionError if the transition is not valid.
func (t *Task) Transition(to State) error {
	if err := ValidateTaskTransition(t.State, to); err != nil {
		return err
	}
	if t.State == to {
		return nil
	}

	now := time.Now().UTC()
	t.State = to
	t.UpdatedAt = now

	if to == StateInitializing && t.StartedAt == nil {
		t.StartedAt = &now
	}

	if to.IsTerminal() && t.FinishedAt == nil {
		t.FinishedAt = &now
	}

	return nil
}

// SetExitCode records the exit code from the TES executor log.
// This is the authoritative source — nf-ga4gh reads exit codes from
// logs[0].logs[0].exitCode, not from the .exitcode file.
func (t *Task) SetExitCode(code int32) {
	t.ExitCode = &code
}

// SetError transitions to an error state and optionally records the exit code.
func (t *Task) SetError(to State, exitCode *int32) error {
	if !to.IsError() {
		return &TransitionError{From: t.State, To: to, Reason: "SetError requires an error state"}
	}
	if err := t.Transition(to); err != nil {
		return err
	}
	if exitCode != nil {
		t.ExitCode = exitCode
	}
	return nil
}
