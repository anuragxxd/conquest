package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anuragxxd/conquest/internal/backend"
	nfengine "github.com/anuragxxd/conquest/internal/engine/nextflow"
	"github.com/anuragxxd/conquest/internal/source"
	"github.com/anuragxxd/conquest/internal/storage"
)

type NextflowRunAdapterConfig struct {
	DefaultRunnerImage    string
	RunnerImagesByVersion map[string]string
	InternalBaseURL       string
	S3Endpoint            string
	S3Bucket              string
	S3Region              string
}

type NextflowRunAdapter struct {
	cfg NextflowRunAdapterConfig
}

func NewNextflowRunAdapter(cfg NextflowRunAdapterConfig) *NextflowRunAdapter {
	return &NextflowRunAdapter{cfg: cfg}
}

func (a *NextflowRunAdapter) Name() string {
	return "nextflow"
}

func (a *NextflowRunAdapter) PrepareRun(runID string, req SubmissionRequest, layout storage.Layout) (*PreparedRun, error) {
	var nfParams source.NextflowEngineParams
	if len(req.WorkflowEngineParameters) > 0 {
		if err := json.Unmarshal(req.WorkflowEngineParameters, &nfParams); err != nil {
			return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: "invalid JSON: " + err.Error()}
		}
	}

	profiles, err := source.NormalizeProfiles(nfParams.Profiles)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: err.Error()}
	}
	nfParams.Profiles = profiles

	runtime, err := nfengine.ResolveRuntime(req.WorkflowEngineVersion)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_version", Msg: err.Error()}
	}

	runnerImage := a.resolveRunnerImage(runtime.Version)
	if runnerImage == "" {
		return nil, &ValidationError{Field: "workflow_engine_version", Msg: fmt.Sprintf("nextflow version %q is not configured for execution in this deployment", runtime.Version)}
	}

	resolved, err := source.Resolve(req.WorkflowURL, &nfParams)
	if err != nil {
		field := "workflow_url"
		if strings.Contains(err.Error(), "revision") {
			field = "workflow_engine_parameters"
		}
		return nil, &ValidationError{Field: field, Msg: err.Error()}
	}

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

	engineParams, err := json.Marshal(nfParams)
	if err != nil {
		return nil, fmt.Errorf("marshal nextflow engine params: %w", err)
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
		"runner", "nextflow",
		"--run-id", runID,
		"--nextflow-version", runtime.Version,
		"--repo-url", resolved.RepoURL,
		"--revision", nfParams.Revision,
		"--work-dir-prefix", layout.NextflowWorkDirURI(runID),
		"--output-dir-prefix", layout.OutputsDirURI(runID),
		"--run-log-key", layout.RunLog(runID),
		"--internal-tes-base", strings.TrimRight(a.cfg.InternalBaseURL, "/") + "/internal/runs/" + runID,
		"--s3-endpoint", a.cfg.S3Endpoint,
		"--s3-bucket", a.cfg.S3Bucket,
		"--s3-region", a.cfg.S3Region,
	}

	if nfParams.Entry != "" {
		args = append(args, "--entry", nfParams.Entry)
	}
	if nfParams.MainScript != "" {
		args = append(args, "--main-script", nfParams.MainScript)
	}
	if len(nfParams.Profiles) > 0 {
		args = append(args, "--profiles", strings.Join(nfParams.Profiles, ","))
	}

	env := make([]backend.EnvVar, 0, 2)
	if len(workflowParams) > 0 {
		env = append(env, backend.EnvVar{Name: "CONQUEST_WORKFLOW_PARAMS", Value: string(workflowParams)})
	}
	if len(runtime.AllowedPlugins) > 0 {
		env = append(env, backend.EnvVar{Name: "NXF_PLUGINS_ALLOWED", Value: strings.Join(runtime.AllowedPlugins, ",")})
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

func (a *NextflowRunAdapter) resolveRunnerImage(version string) string {
	if image := strings.TrimSpace(a.cfg.RunnerImagesByVersion[version]); image != "" {
		return image
	}
	if version == nfengine.DefaultVersion() {
		return strings.TrimSpace(a.cfg.DefaultRunnerImage)
	}
	return ""
}
