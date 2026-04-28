package adapter

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/anuragxxd/conquest/internal/backend"
	wdlengine "github.com/anuragxxd/conquest/internal/engine/wdl"
	"github.com/anuragxxd/conquest/internal/source"
	"github.com/anuragxxd/conquest/internal/storage"
)

type WDLRunAdapterConfig struct {
	DefaultRunnerImage    string
	RunnerImagesByVersion map[string]string
	InternalBaseURL       string
	S3Endpoint            string
	S3Bucket              string
	S3Region              string
	WorkspaceMode         string
	AllowExternalImports  bool
}

type WDLRunAdapter struct {
	cfg WDLRunAdapterConfig
}

func NewWDLRunAdapter(cfg WDLRunAdapterConfig) *WDLRunAdapter {
	return &WDLRunAdapter{cfg: cfg}
}

func (a *WDLRunAdapter) Name() string {
	return "wdl"
}

func (a *WDLRunAdapter) PrepareRun(runID string, req SubmissionRequest, layout storage.Layout) (*PreparedRun, error) {
	if err := wdlengine.ValidateWorkflowVersion(req.WorkflowTypeVersion); err != nil {
		return nil, &ValidationError{Field: "workflow_type_version", Msg: err.Error()}
	}
	if a.cfg.WorkspaceMode != "pvc" && a.cfg.WorkspaceMode != "objectfs" {
		return nil, &ValidationError{Field: "workflow_type", Msg: "WDL requires shared workspace mode (CONQUEST_WORKSPACE_MODE=pvc or objectfs)"}
	}

	var wdlParams source.WDLEngineParams
	if len(req.WorkflowEngineParameters) > 0 {
		if err := json.Unmarshal(req.WorkflowEngineParameters, &wdlParams); err != nil {
			return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: "invalid JSON: " + err.Error()}
		}
	}

	runtime, err := wdlengine.ResolveRuntime(req.WorkflowEngineVersion)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_version", Msg: err.Error()}
	}

	runnerImage := a.resolveRunnerImage(runtime.Version)
	if runnerImage == "" {
		return nil, &ValidationError{Field: "workflow_engine_version", Msg: fmt.Sprintf("miniwdl version %q is not configured for execution in this deployment", runtime.Version)}
	}

	resolved, err := source.ResolveRepository(req.WorkflowURL, wdlParams.Revision)
	if err != nil {
		field := "workflow_url"
		if strings.Contains(err.Error(), "revision") {
			field = "workflow_engine_parameters"
		}
		return nil, &ValidationError{Field: field, Msg: err.Error()}
	}

	mainWorkflowPath, err := normalizeMainWorkflowPath(wdlParams.MainWorkflowPath)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: err.Error()}
	}
	wdlParams.MainWorkflowPath = mainWorkflowPath

	workflowParamsMap, err := normalizeWorkflowParams(req.WorkflowParams)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_params", Msg: err.Error()}
	}
	var workflowParams json.RawMessage
	if len(workflowParamsMap) > 0 {
		workflowParams, err = json.Marshal(workflowParamsMap)
		if err != nil {
			return nil, fmt.Errorf("marshal workflow params: %w", err)
		}
	}

	engineParams, err := json.Marshal(wdlParams)
	if err != nil {
		return nil, fmt.Errorf("marshal wdl engine params: %w", err)
	}

	normalizedReq := struct {
		WorkflowType             string            `json:"workflow_type"`
		WorkflowTypeVersion      string            `json:"workflow_type_version"`
		WorkflowURL              string            `json:"workflow_url"`
		WorkflowParams           json.RawMessage   `json:"workflow_params,omitempty"`
		WorkflowEngine           string            `json:"workflow_engine,omitempty"`
		WorkflowEngineVersion    string            `json:"workflow_engine_version,omitempty"`
		WorkflowEngineParameters json.RawMessage   `json:"workflow_engine_parameters,omitempty"`
		Tags                     map[string]string `json:"tags,omitempty"`
	}{
		WorkflowType:             req.WorkflowType,
		WorkflowTypeVersion:      req.WorkflowTypeVersion,
		WorkflowURL:              resolved.RepoURL,
		WorkflowParams:           workflowParams,
		WorkflowEngine:           a.Name(),
		WorkflowEngineVersion:    runtime.Version,
		WorkflowEngineParameters: engineParams,
		Tags:                     req.Tags,
	}

	payload, err := json.Marshal(normalizedReq)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized run request: %w", err)
	}

	args := []string{
		"runner", "wdl",
		"--run-id", runID,
		"--repo-url", resolved.RepoURL,
		"--revision", wdlParams.Revision,
		"--main-workflow-path", wdlParams.MainWorkflowPath,
		"--output-dir-prefix", layout.OutputsDirURI(runID),
		"--run-log-key", layout.RunLog(runID),
		"--workflow-outputs-key", layout.WorkflowOutputs(runID),
		"--engine-metadata-key", layout.EngineMetadata(runID),
		"--internal-tes-base", strings.TrimRight(a.cfg.InternalBaseURL, "/") + "/internal/runs/" + runID,
		"--s3-endpoint", a.cfg.S3Endpoint,
		"--s3-bucket", a.cfg.S3Bucket,
		"--s3-region", a.cfg.S3Region,
	}
	if a.cfg.AllowExternalImports {
		args = append(args, "--allow-external-imports")
	}

	env := make([]backend.EnvVar, 0, 1)
	if len(workflowParams) > 0 {
		env = append(env, backend.EnvVar{Name: "CONQUEST_WORKFLOW_PARAMS", Value: string(workflowParams)})
	}

	return &PreparedRun{
		WorkflowEngine:        a.Name(),
		WorkflowEngineVersion: runtime.Version,
		RequestPayload:        payload,
		ResolvedWorkflowRef:   resolved.Ref,
		RunnerSpec: backend.RunnerSpec{
			Image:   runnerImage,
			Command: []string{"/usr/local/bin/conquest"},
			Args:    args,
			Env:     env,
		},
	}, nil
}

func (a *WDLRunAdapter) resolveRunnerImage(version string) string {
	if image := strings.TrimSpace(a.cfg.RunnerImagesByVersion[version]); image != "" {
		return image
	}
	if version == wdlengine.DefaultVersion() {
		return strings.TrimSpace(a.cfg.DefaultRunnerImage)
	}
	return ""
}

func normalizeMainWorkflowPath(value string) (string, error) {
	if err := validateWorkflowRelativePath("main_workflow_path", value); err != nil {
		return "", err
	}
	cleaned := path.Clean(strings.TrimSpace(value))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("main_workflow_path is required")
	}
	if !strings.HasSuffix(strings.ToLower(cleaned), ".wdl") {
		return "", fmt.Errorf("main_workflow_path must point to a .wdl file")
	}
	return cleaned, nil
}
