package domain

import (
	"encoding/json"
	"time"
)

// Run represents a WES workflow run.
//
// It is the authoritative record for a single workflow execution. The state
// machine follows RunTransitions. Timestamp semantics:
//
//   - CreatedAt:  set once at creation time (immutable).
//   - UpdatedAt:  set on every state change.
//   - StartedAt:  set when the state first reaches INITIALIZING.
//   - FinishedAt: set when the state reaches any terminal state.
type Run struct {
	RunID                 string          `json:"run_id"`
	WorkflowType          string          `json:"workflow_type"`           // e.g. "NFL" (Nextflow), "SMK" (Snakemake)
	WorkflowTypeVersion   string          `json:"workflow_type_version"`   // e.g. "DSL2"
	WorkflowEngine        string          `json:"workflow_engine"`         // e.g. "nextflow"
	WorkflowEngineVersion string          `json:"workflow_engine_version"` // e.g. "24.10.0"
	State                 State           `json:"state"`
	RequestPayload        json.RawMessage `json:"request_payload"`         // original WES RunRequest (verbatim JSON)
	ResolvedWorkflowRef   string          `json:"resolved_workflow_ref"`   // e.g. "https://github.com/org/repo@<sha>"
	OutputPrefix          string          `json:"output_prefix"`           // s3://bucket/v1/workflow-runs/<run-id>/outputs/
	LayoutVersion         string          `json:"layout_version"`          // storage layout version, e.g. "v1"
	RunRootURI            string          `json:"run_root_uri"`            // s3://bucket/v1/workflow-runs/<run-id>/
	BackendRef            string          `json:"backend_ref,omitempty"`   // K8s runner Job name
	BackendState          json.RawMessage `json:"backend_state,omitempty"` // Runner pod/job state blob
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	StartedAt             *time.Time      `json:"started_at,omitempty"`
	FinishedAt            *time.Time      `json:"finished_at,omitempty"`
	ErrorSummary          string          `json:"error_summary,omitempty"`
}

// NewRun creates a Run in QUEUED state with a fresh UUIDv7 ID.
func NewRun(
	workflowType, workflowTypeVersion string,
	engine, engineVersion string,
	requestPayload json.RawMessage,
	outputPrefix, layoutVersion, runRootURI string,
) *Run {
	return NewRunWithID(
		NewID(),
		workflowType,
		workflowTypeVersion,
		engine,
		engineVersion,
		requestPayload,
		outputPrefix,
		layoutVersion,
		runRootURI,
	)
}

func NewRunWithID(
	runID string,
	workflowType, workflowTypeVersion string,
	engine, engineVersion string,
	requestPayload json.RawMessage,
	outputPrefix, layoutVersion, runRootURI string,
) *Run {
	now := time.Now().UTC()
	return &Run{
		RunID:                 runID,
		WorkflowType:          workflowType,
		WorkflowTypeVersion:   workflowTypeVersion,
		WorkflowEngine:        engine,
		WorkflowEngineVersion: engineVersion,
		State:                 StateQueued,
		RequestPayload:        requestPayload,
		OutputPrefix:          outputPrefix,
		LayoutVersion:         layoutVersion,
		RunRootURI:            runRootURI,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

// Transition moves the run to a new state, updating timestamps as needed.
// Returns a *TransitionError if the transition is not valid.
func (r *Run) Transition(to State) error {
	if err := ValidateRunTransition(r.State, to); err != nil {
		return err
	}
	if r.State == to {
		return nil // no-op
	}

	now := time.Now().UTC()
	r.State = to
	r.UpdatedAt = now

	// StartedAt: set when we first enter INITIALIZING.
	if to == StateInitializing && r.StartedAt == nil {
		r.StartedAt = &now
	}

	// FinishedAt: set when entering any terminal state.
	if to.IsTerminal() && r.FinishedAt == nil {
		r.FinishedAt = &now
	}

	return nil
}

// SetError is a convenience that transitions to the given error state and
// records an error summary.
func (r *Run) SetError(to State, summary string) error {
	if !to.IsError() {
		return &TransitionError{From: r.State, To: to, Reason: "SetError requires an error state"}
	}
	if err := r.Transition(to); err != nil {
		return err
	}
	r.ErrorSummary = summary
	return nil
}
