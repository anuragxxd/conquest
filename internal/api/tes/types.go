// Package tes defines the GA4GH TES v1.1 API types and HTTP handlers.
package tes

import (
	"encoding/json"

	"github.com/anuragxxd/conquest/internal/domain"
)

// ---------------------------------------------------------------------------
// Task
// ---------------------------------------------------------------------------

// Task is the top-level TES task object.
type Task struct {
	ID           string          `json:"id,omitempty"`
	State        domain.State    `json:"state,omitempty"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Executors    []Executor      `json:"executors,omitempty"`
	Inputs       []Input         `json:"inputs,omitempty"`
	Outputs      []Output        `json:"outputs,omitempty"`
	Resources    *Resources      `json:"resources,omitempty"`
	Volumes      []string        `json:"volumes,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Logs         []TaskLog       `json:"logs,omitempty"`
	CreationTime string          `json:"creation_time,omitempty"`
}

// Executor describes a command to run inside a container.
type Executor struct {
	Image       string            `json:"image"`
	Command     []string          `json:"command"`
	Workdir     string            `json:"workdir,omitempty"`
	Stdin       string            `json:"stdin,omitempty"`
	Stdout      string            `json:"stdout,omitempty"`
	Stderr      string            `json:"stderr,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	IgnoreError bool              `json:"ignore_error,omitempty"`
}

// Input describes a file to be staged into the executor's container.
type Input struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path"`
	Type        string `json:"type,omitempty"`    // FILE or DIRECTORY
	Content     string `json:"content,omitempty"` // inline content (alternative to URL)
	Streamable  bool   `json:"streamable,omitempty"`
}

// Output describes a file to be uploaded from the executor's container.
type Output struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	PathPrefix  string `json:"path_prefix,omitempty"`
	Type        string `json:"type,omitempty"` // FILE or DIRECTORY
}

// Resources describes compute resources for a task.
type Resources struct {
	CPUCores              *int32            `json:"cpu_cores,omitempty"`
	RAMGb                 *float64          `json:"ram_gb,omitempty"`
	DiskGb                *float64          `json:"disk_gb,omitempty"`
	Preemptible           *bool             `json:"preemptible,omitempty"`
	Zones                 []string          `json:"zones,omitempty"`
	BackendParameters     map[string]string `json:"backend_parameters,omitempty"`
	BackendParametersStrict *bool           `json:"backend_parameters_strict,omitempty"`
}

// ---------------------------------------------------------------------------
// Task logs
// ---------------------------------------------------------------------------

// TaskLog is one entry in the task's logs array. A new entry is appended
// on each retry attempt.
type TaskLog struct {
	Logs       []ExecutorLog     `json:"logs,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	StartTime  string            `json:"start_time,omitempty"`
	EndTime    string            `json:"end_time,omitempty"`
	Outputs    []OutputFileLog   `json:"outputs,omitempty"`
	SystemLogs []string          `json:"system_logs,omitempty"`
}

// ExecutorLog captures the output of a single executor run.
type ExecutorLog struct {
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	Stdout    string `json:"stdout,omitempty"` // convenience snippet, no completeness guarantee
	Stderr    string `json:"stderr,omitempty"` // convenience snippet, no completeness guarantee
	ExitCode  *int32 `json:"exit_code,omitempty"`
}

// OutputFileLog records details of an output file after upload.
type OutputFileLog struct {
	URL       string `json:"url"`
	Path      string `json:"path"`
	SizeBytes string `json:"size_bytes,omitempty"` // string-encoded int64 per spec
}

// ---------------------------------------------------------------------------
// Service info
// ---------------------------------------------------------------------------

// ServiceInfo is the response for GET /service-info.
type ServiceInfo struct {
	Name             string          `json:"name,omitempty"`
	Doc              string          `json:"doc,omitempty"`
	Storage          []string        `json:"storage,omitempty"`
	Type             *ServiceType    `json:"type,omitempty"`
	Organization     json.RawMessage `json:"organization,omitempty"`
	TESResourcesBackendParameters []string `json:"tes_resources_backend_parameters,omitempty"`
}

// ServiceType identifies the service.
type ServiceType struct {
	Group    string `json:"group"`
	Artifact string `json:"artifact"`
	Version  string `json:"version"`
}

// ---------------------------------------------------------------------------
// Request / response wrappers
// ---------------------------------------------------------------------------

// CreateTaskResponse is returned from POST /tasks.
type CreateTaskResponse struct {
	ID string `json:"id"`
}

// ListTasksResponse is returned from GET /tasks.
type ListTasksResponse struct {
	Tasks         []Task `json:"tasks"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// CancelTaskResponse is returned from POST /tasks/{id}:cancel.
type CancelTaskResponse struct{}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

// ErrorResponse is the standard error body.
type ErrorResponse struct {
	Msg        string `json:"msg"`
	StatusCode int    `json:"status_code"`
}
