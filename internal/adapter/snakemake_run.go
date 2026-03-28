package adapter

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/anuragxxd/conquest/internal/backend"
	smkengine "github.com/anuragxxd/conquest/internal/engine/snakemake"
	"github.com/anuragxxd/conquest/internal/source"
	"github.com/anuragxxd/conquest/internal/storage"
)

type SnakemakeRunAdapterConfig struct {
	RunnerImage     string
	TaskImage       string
	DefaultJobs     int
	DefaultDiskMB   int
	InternalBaseURL string
	S3Endpoint      string
	S3Bucket        string
	S3Region        string
}

type SnakemakeRunAdapter struct {
	cfg SnakemakeRunAdapterConfig
}

func NewSnakemakeRunAdapter(cfg SnakemakeRunAdapterConfig) *SnakemakeRunAdapter {
	return &SnakemakeRunAdapter{cfg: cfg}
}

func (a *SnakemakeRunAdapter) Name() string {
	return "snakemake"
}

func (a *SnakemakeRunAdapter) PrepareRun(runID string, req SubmissionRequest, layout storage.Layout) (*PreparedRun, error) {
	var smkParams source.SnakemakeEngineParams
	if len(req.WorkflowEngineParameters) > 0 {
		if err := json.Unmarshal(req.WorkflowEngineParameters, &smkParams); err != nil {
			return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: "invalid JSON: " + err.Error()}
		}
	}

	runtime, err := smkengine.ResolveRuntime(req.WorkflowEngineVersion)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_version", Msg: err.Error()}
	}

	if strings.TrimSpace(a.cfg.RunnerImage) == "" {
		return nil, &ValidationError{Field: "workflow_engine", Msg: "snakemake execution is not configured in this deployment"}
	}

	resolved, err := source.ResolveRepository(req.WorkflowURL, smkParams.Revision)
	if err != nil {
		field := "workflow_url"
		if strings.Contains(err.Error(), "revision") {
			field = "workflow_engine_parameters"
		}
		return nil, &ValidationError{Field: field, Msg: err.Error()}
	}

	if err := validateWorkflowRelativePath("snakefile", smkParams.Snakefile); err != nil {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: err.Error()}
	}
	if err := validateWorkflowRelativePath("directory", smkParams.Directory); err != nil {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: err.Error()}
	}

	normalizedTargets, err := normalizeTargets(smkParams.Targets)
	if err != nil {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: err.Error()}
	}
	smkParams.Targets = normalizedTargets

	jobs := smkParams.Jobs
	if jobs == 0 {
		jobs = a.cfg.DefaultJobs
	}
	if jobs <= 0 {
		return nil, &ValidationError{Field: "workflow_engine_parameters", Msg: "jobs must be greater than zero"}
	}
	smkParams.Jobs = jobs

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

	engineParams, err := json.Marshal(smkParams)
	if err != nil {
		return nil, fmt.Errorf("marshal snakemake engine params: %w", err)
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
		"runner", "snakemake",
		"--run-id", runID,
		"--snakemake-version", runtime.Version,
		"--repo-url", resolved.RepoURL,
		"--revision", smkParams.Revision,
		"--workflow-bundle-prefix", layout.WorkflowBundleDirURI(runID),
		"--work-dir-prefix", layout.SnakemakeWorkDirURI(runID),
		"--output-dir-prefix", layout.OutputsDirURI(runID),
		"--run-log-key", layout.RunLog(runID),
		"--internal-tes-base", strings.TrimRight(a.cfg.InternalBaseURL, "/") + "/internal/runs/" + runID,
		"--s3-endpoint", a.cfg.S3Endpoint,
		"--s3-bucket", a.cfg.S3Bucket,
		"--s3-region", a.cfg.S3Region,
		"--jobs", fmt.Sprintf("%d", jobs),
		"--default-disk-mb", fmt.Sprintf("%d", a.cfg.DefaultDiskMB),
	}

	if snakefile := strings.TrimSpace(smkParams.Snakefile); snakefile != "" {
		args = append(args, "--snakefile", snakefile)
	}
	if directory := strings.TrimSpace(smkParams.Directory); directory != "" {
		args = append(args, "--directory", directory)
	}
	if len(smkParams.Targets) > 0 {
		args = append(args, "--targets", strings.Join(smkParams.Targets, ","))
	}

	env := make([]backend.EnvVar, 0, 2)
	if len(workflowParams) > 0 {
		env = append(env, backend.EnvVar{Name: "CONQUEST_WORKFLOW_PARAMS", Value: string(workflowParams)})
	}
	if strings.TrimSpace(a.cfg.TaskImage) != "" {
		env = append(env, backend.EnvVar{Name: "CONQUEST_SNAKEMAKE_TASK_IMAGE", Value: strings.TrimSpace(a.cfg.TaskImage)})
	}

	return &PreparedRun{
		WorkflowEngine:        a.Name(),
		WorkflowEngineVersion: runtime.Version,
		RequestPayload:        payload,
		ResolvedWorkflowRef:   resolved.Ref,
		RunnerSpec: backend.RunnerSpec{
			Image:   strings.TrimSpace(a.cfg.RunnerImage),
			Command: []string{"/usr/local/bin/conquest"},
			Args:    args,
			Env:     env,
		},
	}, nil
}

func validateWorkflowRelativePath(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if path.IsAbs(value) {
		return fmt.Errorf("%s must be a workflow-relative path", name)
	}
	cleaned := path.Clean(value)
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return fmt.Errorf("%s must not traverse outside the workflow root", name)
	}
	return nil
}

func normalizeTargets(targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(targets))
	for i, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			return nil, fmt.Errorf("targets[%d] must not be empty", i)
		}
		normalized = append(normalized, target)
	}
	return normalized, nil
}
