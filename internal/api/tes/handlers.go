package tes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	wdlengine "github.com/anuragxxd/conquest/internal/engine/wdl"
	"github.com/anuragxxd/conquest/internal/repository"
	"github.com/anuragxxd/conquest/internal/repository/pg"
	"github.com/anuragxxd/conquest/internal/taskpolicy"
)

type taskValidationOptions struct {
	AllowSharedLocalPaths bool
}

// Handler holds the TES HTTP handlers.
type Handler struct {
	runs    repository.RunStore
	tasks   repository.TaskStore
	events  repository.EventStore
	backend backend.TaskBackend // nil if no backend configured (persistence-only mode)
	policy  *taskpolicy.Policy  // task normalization/validation policy
}

// NewHandler creates TES handlers.
func NewHandler(runs repository.RunStore, tasks repository.TaskStore, events repository.EventStore, be backend.TaskBackend) *Handler {
	return &Handler{runs: runs, tasks: tasks, events: events, backend: be, policy: &taskpolicy.Policy{}}
}

// SetPolicy configures the task normalization/validation policy.
// If not called, a zero-value policy is used (passthrough for valid tasks).
func (h *Handler) SetPolicy(p *taskpolicy.Policy) {
	if p != nil {
		h.policy = p
	}
}

// Routes registers public TES routes on the given chi router.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/ga4gh/tes/v1/service-info", h.GetServiceInfo)
	r.Get("/ga4gh/tes/service-info", h.GetServiceInfo)
	r.Post("/ga4gh/tes/v1/tasks", h.CreateTask)
	r.Get("/ga4gh/tes/v1/tasks", h.ListTasks)
	r.Get("/ga4gh/tes/v1/tasks/{id}", h.GetTask)
	r.Post("/ga4gh/tes/v1/tasks/{id}:cancel", h.CancelTask)
}

// InternalRoutes registers run-scoped internal TES routes.
// These are used by the Nextflow runner to submit tasks bound to a specific
// WES run, without relying on TES tags or stub-run creation.
func (h *Handler) InternalRoutes(r chi.Router) {
	r.Post("/internal/runs/{run_id}/ga4gh/tes/v1/tasks", h.CreateTaskForRun)
	r.Get("/internal/runs/{run_id}/ga4gh/tes/v1/tasks/{id}", h.GetTask)
	r.Post("/internal/runs/{run_id}/ga4gh/tes/v1/tasks/{id}:cancel", h.CancelTask)
	// Service info — same as public. The non-/v1 alias is for nf-ga4gh's
	// generated client, which hardcodes /service-info relative to tes.endpoint.
	r.Get("/internal/runs/{run_id}/ga4gh/tes/v1/service-info", h.GetServiceInfo)
	r.Get("/internal/runs/{run_id}/ga4gh/tes/service-info", h.GetServiceInfo)
}

func (h *Handler) GetServiceInfo(w http.ResponseWriter, r *http.Request) {
	info := ServiceInfo{
		Name:    "conquest-tes",
		Doc:     "Conquest TES backend (GA4GH TES v1.1)",
		Storage: []string{"s3"},
		Type: &ServiceType{
			Group:    "org.ga4gh",
			Artifact: "tes",
			Version:  "1.1.0",
		},
	}
	writeJSON(w, http.StatusOK, info)
}

// CreateTaskForRun handles POST /internal/runs/{run_id}/ga4gh/tes/v1/tasks.
// The run_id is taken from the URL path, not from TES tags.
func (h *Handler) CreateTaskForRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required in path")
		return
	}

	// Load the full run to check state and copy workspace binding.
	run, err := h.runs.Get(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if run.State.IsTerminal() {
		writeError(w, http.StatusConflict, "run is in terminal state")
		return
	}

	h.createTaskWithRun(w, r, run)
}

func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req Task
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if errMsg := validateTESTask(&req); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	// Determine run_id from tags if provided (set by engine adapter).
	runID := ""
	if req.Tags != nil {
		runID = req.Tags["conquest_run_id"]
	}

	// For standalone TES usage (no WES run), create a stub run so the FK
	// constraint on tasks.run_id is satisfied.
	var workspace *domain.WorkspaceBinding
	if runID == "" {
		stubRun := domain.NewRun(
			"TES",        // workflow_type
			"standalone", // workflow_type_version
			"tes",        // engine
			"",           // engine_version
			json.RawMessage("{}"),
			"",   // output_prefix
			"v1", // layout_version
			"",   // run_root_uri
		)
		if err := h.runs.Create(r.Context(), stubRun); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create stub run: "+err.Error())
			return
		}
		runID = stubRun.RunID
	} else {
		// Load the run to inherit workspace binding.
		run, err := h.runs.Get(r.Context(), runID)
		if err == nil {
			workspace = run.Workspace
		}
	}

	h.createAndSubmitTask(w, r, &req, runID, workspace)
}

// createTaskWithRunID is the shared implementation for both public and
// internal task creation. The runID is already validated.
func (h *Handler) createTaskWithRun(w http.ResponseWriter, r *http.Request, run *domain.Run) {
	var req Task
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	ensureInternalRunTaskDefaults(&req, run)
	if errMsg := validateTESTaskWithOptions(&req, validationOptionsForRun(run)); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	h.createAndSubmitTask(w, r, &req, run.RunID, run.Workspace)
}

// createAndSubmitTask persists and submits a TES task.
func (h *Handler) createAndSubmitTask(w http.ResponseWriter, r *http.Request, req *Task, runID string, workspace *domain.WorkspaceBinding) {
	// Apply platform policy: normalize image refs, apply resource
	// defaults/floors/ceilings, and validate before touching K8s.
	if h.policy != nil {
		if err := applyPolicy(h.policy, req); err != nil {
			writeError(w, http.StatusBadRequest, "task policy: "+err.Error())
			return
		}
	}

	requestPayload, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal task request")
		return
	}

	task := domain.NewTask(runID)
	task.TESTaskID = task.TaskID
	task.EngineTaskName = req.Name
	task.TaskRequestPayload = requestPayload
	task.Workspace = workspace // inherit from run (nil for emptydir)

	if req.Resources != nil {
		resJSON, _ := json.Marshal(req.Resources)
		task.Resources = resJSON
	}

	if err := h.tasks.Create(r.Context(), task); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	event := domain.NewEvent(runID, task.TaskID, domain.EventTaskCreated, nil)
	_ = h.events.Append(r.Context(), event)

	// If a backend is configured, submit the task for execution.
	if h.backend != nil {
		if err := h.backend.Submit(r.Context(), task); err != nil {
			if updateErr := h.tasks.UpdateState(r.Context(), task.TaskID, domain.StateQueued, domain.StateSystemError); updateErr != nil {
				writeError(w, http.StatusInternalServerError, "task created but submission failed, and state update also failed: "+updateErr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "task created but submission failed: "+err.Error())
			return
		}
		if err := h.tasks.SetBackendRef(r.Context(), task.TaskID, task.BackendRef); err != nil {
			_ = h.backend.Cancel(context.Background(), task)
			_ = h.tasks.UpdateState(r.Context(), task.TaskID, domain.StateQueued, domain.StateSystemError)
			writeError(w, http.StatusInternalServerError, "task created and submitted, but failed to persist backend reference: "+err.Error())
			return
		}
		if err := h.tasks.UpdateState(r.Context(), task.TaskID, domain.StateQueued, domain.StateInitializing); err != nil {
			_ = h.backend.Cancel(context.Background(), task)
			_ = h.tasks.UpdateState(r.Context(), task.TaskID, domain.StateQueued, domain.StateSystemError)
			writeError(w, http.StatusInternalServerError, "task created and submitted, but failed to persist initializing state: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, CreateTaskResponse{ID: task.TESTaskID})
}

// validateTESTask performs v1 scope validation on a TES task request.
func validateTESTask(req *Task) string {
	return validateTESTaskWithOptions(req, taskValidationOptions{})
}

func validateTESTaskWithOptions(req *Task, options taskValidationOptions) string {
	if len(req.Executors) == 0 {
		return "at least one executor is required"
	}
	if len(req.Executors) > 1 {
		return "multiple executors not supported in v1 (max 1)"
	}
	exec := req.Executors[0]
	if exec.Image == "" {
		return "executor image is required"
	}
	if len(exec.Command) == 0 {
		return "executor command is required"
	}
	if len(req.Volumes) > 0 {
		return "task volumes not supported in v1"
	}
	for i, input := range req.Inputs {
		if input.Content != "" {
			continue
		}
		if input.URL == "" {
			return fmt.Sprintf("input[%d] must have url or content", i)
		}
		if !isSupportedTaskInputURL(input.URL, options) {
			if options.AllowSharedLocalPaths {
				return fmt.Sprintf("input[%d] url must be s3://, http://, https://, or a trusted shared local path (got %q)", i, input.URL)
			}
			return fmt.Sprintf("input[%d] url must be s3://, http://, or https:// (got %q)", i, input.URL)
		}
	}
	for i, output := range req.Outputs {
		if output.URL == "" {
			return fmt.Sprintf("output[%d] url is required", i)
		}
		if output.Path == "" {
			return fmt.Sprintf("output[%d] path is required", i)
		}
	}
	return ""
}

func isSupportedTaskInputURL(raw string, options taskValidationOptions) bool {
	if strings.HasPrefix(raw, "s3://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return true
	}
	if options.AllowSharedLocalPaths {
		_, ok := wdlengine.SharedLocalPathFromURL(raw)
		return ok
	}
	return false
}

func validationOptionsForRun(run *domain.Run) taskValidationOptions {
	return taskValidationOptions{AllowSharedLocalPaths: run != nil && run.Workspace.IsPVC()}
}

func ensureInternalRunTaskDefaults(req *Task, run *domain.Run) {
	if run == nil || !run.Workspace.IsPVC() {
		return
	}
	if req.Tags == nil {
		req.Tags = make(map[string]string, 1)
	}
	if strings.TrimSpace(req.Tags["conquest_workspace_scope"]) == "" {
		req.Tags["conquest_workspace_scope"] = "shared"
	}
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Handle the ":cancel" suffix case for chi routing.
	id = strings.TrimSuffix(id, ":cancel")

	view := r.URL.Query().Get("view")
	if view == "" {
		view = "MINIMAL"
	}

	task, err := h.tasks.Get(r.Context(), id)
	if errors.Is(err, pg.ErrNotFound) {
		// Try by TES ID.
		task, err = h.tasks.GetByTESID(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := domainTaskToTES(task, view)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	pageSize := queryInt(r, "page_size", 50)
	pageToken := r.URL.Query().Get("page_token")
	result, err := h.tasks.List(r.Context(), repository.Page{Size: pageSize, Token: pageToken})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks := make([]Task, 0, len(result.Items))
	for _, task := range result.Items {
		tasks = append(tasks, domainTaskToTES(task, "MINIMAL"))
	}
	writeJSON(w, http.StatusOK, ListTasksResponse{
		Tasks:         tasks,
		NextPageToken: result.NextToken,
	})
}

func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	task, err := h.tasks.Get(r.Context(), id)
	if errors.Is(err, pg.ErrNotFound) {
		task, err = h.tasks.GetByTESID(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if task.State.IsTerminal() {
		// Already done — return success per TES spec.
		writeJSON(w, http.StatusOK, CancelTaskResponse{})
		return
	}

	// Tasks go directly to CANCELED (no CANCELING — see issue #30).
	if err := domain.ValidateTaskTransition(task.State, domain.StateCanceled); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if h.backend != nil {
		if err := h.backend.Cancel(r.Context(), task); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := h.tasks.UpdateState(r.Context(), task.TaskID, task.State, domain.StateCanceled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, CancelTaskResponse{})
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func domainTaskToTES(t *domain.Task, view string) Task {
	resp := Task{
		ID:    t.TESTaskID,
		State: t.State,
	}

	if view == "MINIMAL" {
		return resp
	}

	// BASIC and FULL views include more fields.
	resp.Name = t.EngineTaskName
	resp.CreationTime = t.CreatedAt.Format(time.RFC3339)

	if t.Resources != nil {
		var res Resources
		_ = json.Unmarshal(t.Resources, &res)
		resp.Resources = &res
	}

	// Reconstruct inputs/outputs/executors from the stored request.
	if t.TaskRequestPayload != nil {
		var stored Task
		if json.Unmarshal(t.TaskRequestPayload, &stored) == nil {
			resp.Executors = stored.Executors
			resp.Inputs = stored.Inputs
			resp.Outputs = stored.Outputs
			resp.Volumes = stored.Volumes
			resp.Description = stored.Description
			resp.Tags = stored.Tags
		}
	}

	// FULL view includes logs.
	if view == "FULL" {
		resp.Logs = []TaskLog{
			{
				StartTime: timeStr(t.StartedAt),
				EndTime:   timeStr(t.FinishedAt),
				Logs: []ExecutorLog{
					{
						ExitCode: t.ExitCode,
					},
				},
			},
		}
	}

	return resp
}

func timeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Msg: msg, StatusCode: status})
}

func writeErrorf(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, ErrorResponse{Msg: fmt.Sprintf(format, args...), StatusCode: status})
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// applyPolicy bridges between the TES-typed request and the
// cycle-free taskpolicy package. It converts the relevant fields to
// taskpolicy types, runs the policy, and writes the normalized values
// back into the TES request.
func applyPolicy(p *taskpolicy.Policy, req *Task) error {
	// Build the minimal TaskSpec from the TES request.
	spec := taskpolicy.TaskSpec{
		Executors: make([]taskpolicy.ExecutorSpec, len(req.Executors)),
	}
	for i, exec := range req.Executors {
		spec.Executors[i] = taskpolicy.ExecutorSpec{Image: exec.Image}
	}
	if req.Resources != nil {
		spec.Resources = &taskpolicy.ResourceSpec{
			CPUCores: req.Resources.CPUCores,
			RAMGb:    req.Resources.RAMGb,
			DiskGb:   req.Resources.DiskGb,
		}
	}

	// Apply the policy.
	if err := p.Apply(&spec); err != nil {
		return err
	}

	// Write the normalized values back.
	for i := range req.Executors {
		req.Executors[i].Image = spec.Executors[i].Image
	}
	if spec.Resources != nil {
		if req.Resources == nil {
			req.Resources = &Resources{}
		}
		req.Resources.CPUCores = spec.Resources.CPUCores
		req.Resources.RAMGb = spec.Resources.RAMGb
		req.Resources.DiskGb = spec.Resources.DiskGb
	}

	return nil
}
