// Package wes defines the GA4GH WES v1.1 API types and HTTP handlers.
package wes

import (
	"encoding/json"
	"time"

	"github.com/anuragxxd/conquest/internal/domain"
)

// ---------------------------------------------------------------------------
// Service info
// ---------------------------------------------------------------------------

// ServiceInfo is the response for GET /service-info.
type ServiceInfo struct {
	WorkflowTypeVersions         map[string]TypeVersions `json:"workflow_type_versions"`
	SupportedWESVersions         []string                `json:"supported_wes_versions"`
	SupportedFilesystemProtocols []string                `json:"supported_filesystem_protocols"`
	WorkflowEngineVersions       map[string]string       `json:"workflow_engine_versions"`
	SystemStateCounts            map[string]int          `json:"system_state_counts"`
	AuthInstructionsURL          string                  `json:"auth_instructions_url"`
	Tags                         map[string]string       `json:"tags,omitempty"`
}

// TypeVersions holds the supported versions for a workflow type.
type TypeVersions struct {
	WorkflowTypeVersion []string `json:"workflow_type_version"`
}

// ---------------------------------------------------------------------------
// Run submission
// ---------------------------------------------------------------------------

// RunRequest represents the parsed multipart form fields from POST /runs.
//
// In the WES spec, POST /runs is multipart/form-data. JSON-typed fields
// (workflow_params, tags, workflow_engine_parameters) are transmitted as
// JSON-encoded strings in the form, not as native objects. We parse them
// into json.RawMessage for storage and later use.
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

// ---------------------------------------------------------------------------
// Run responses
// ---------------------------------------------------------------------------

// RunStatus is the minimal run response (GET /runs/{run_id}/status).
type RunStatus struct {
	RunID string       `json:"run_id"`
	State domain.State `json:"state"`
}

// RunSummary extends RunStatus for list responses. RunStatus alone is
// deprecated in WES v1.1; RunSummary will be the only list item in WES v2.
type RunSummary struct {
	RunID     string            `json:"run_id"`
	State     domain.State      `json:"state"`
	StartTime *time.Time        `json:"start_time,omitempty"`
	EndTime   *time.Time        `json:"end_time,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// RunLog is the full run response (GET /runs/{run_id}).
type RunLog struct {
	RunID       string          `json:"run_id"`
	Request     *RunRequest     `json:"request"`
	State       domain.State    `json:"state"`
	RunLog      *Log            `json:"run_log,omitempty"`
	TaskLogsURL string          `json:"task_logs_url"`
	Outputs     json.RawMessage `json:"outputs,omitempty"`
}

// RunListResponse is the response for GET /runs.
type RunListResponse struct {
	Runs          []RunSummary `json:"runs"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

// RunID is returned from POST /runs.
type RunID struct {
	RunID string `json:"run_id"`
}

// ---------------------------------------------------------------------------
// Task responses (WES task endpoints)
// ---------------------------------------------------------------------------

// TaskLog is the response for GET /runs/{run_id}/tasks/{task_id}.
type TaskLog struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	EndTime    string   `json:"end_time,omitempty"`
	Stdout     string   `json:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	ExitCode   *int32   `json:"exit_code,omitempty"`
	SystemLogs []string `json:"system_logs,omitempty"`
	TesURI     string   `json:"tes_uri,omitempty"`
}

// TaskListResponse is the response for GET /runs/{run_id}/tasks.
type TaskListResponse struct {
	TaskLogs      []TaskLog `json:"task_logs"`
	NextPageToken string    `json:"next_page_token,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared log type
// ---------------------------------------------------------------------------

// Log is used for run-level and task-level logging.
type Log struct {
	Name       string   `json:"name,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	EndTime    string   `json:"end_time,omitempty"`
	Stdout     string   `json:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	ExitCode   *int32   `json:"exit_code,omitempty"`
	SystemLogs []string `json:"system_logs,omitempty"`
}

// ---------------------------------------------------------------------------
// Error response
// ---------------------------------------------------------------------------

// ErrorResponse is the standard error body for all WES error responses.
type ErrorResponse struct {
	Msg        string `json:"msg"`
	StatusCode int    `json:"status_code"`
}
