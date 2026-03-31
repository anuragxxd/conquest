package adapter

import (
	"encoding/json"
	"testing"

	"github.com/anuragxxd/conquest/internal/storage"
)

func TestWDLRunAdapter_PrepareRun_DefaultVersionAndImage(t *testing.T) {
	adapter := NewWDLRunAdapter(WDLRunAdapterConfig{
		DefaultRunnerImage:   "example.com/conquest-cromwell-92-runner:latest",
		InternalBaseURL:      "http://conquest.services.svc.cluster.local",
		S3Endpoint:           "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:             "conquest",
		S3Region:             "us-east-1",
		WorkspaceMode:        "pvc",
		AllowExternalImports: false,
	})

	prepared, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:        "WDL",
		WorkflowTypeVersion: "1.1",
		WorkflowURL:         "https://github.com/acme-org/my-wdl-pipelines.git",
		WorkflowEngineParameters: json.RawMessage(`{
			"revision": "13328f73915ceda9bb794caee2f662230c5a33b6",
			"main_workflow_path": "workflows/rnaseq/main.wdl"
		}`),
		WorkflowParams: json.RawMessage(`{"MyWorkflow.input_bam":"s3://example-bucket/data/sample.bam"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prepared.WorkflowEngine != "cromwell" {
		t.Fatalf("workflow engine = %q", prepared.WorkflowEngine)
	}
	if prepared.WorkflowEngineVersion != "92" {
		t.Fatalf("workflow engine version = %q", prepared.WorkflowEngineVersion)
	}
	if prepared.RunnerSpec.Image != "example.com/conquest-cromwell-92-runner:latest" {
		t.Fatalf("runner image = %q", prepared.RunnerSpec.Image)
	}

	var normalized struct {
		WorkflowURL              string          `json:"workflow_url"`
		WorkflowEngine           string          `json:"workflow_engine"`
		WorkflowEngineVersion    string          `json:"workflow_engine_version"`
		WorkflowEngineParameters json.RawMessage `json:"workflow_engine_parameters"`
	}
	if err := json.Unmarshal(prepared.RequestPayload, &normalized); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if normalized.WorkflowURL != "https://github.com/acme-org/my-wdl-pipelines" {
		t.Fatalf("workflow_url = %q", normalized.WorkflowURL)
	}
	if normalized.WorkflowEngine != "cromwell" {
		t.Fatalf("workflow_engine = %q", normalized.WorkflowEngine)
	}
	if normalized.WorkflowEngineVersion != "92" {
		t.Fatalf("workflow_engine_version = %q", normalized.WorkflowEngineVersion)
	}

	foundMainPath := false
	foundOutputsKey := false
	for i, arg := range prepared.RunnerSpec.Args {
		if arg == "--main-workflow-path" && i+1 < len(prepared.RunnerSpec.Args) && prepared.RunnerSpec.Args[i+1] == "workflows/rnaseq/main.wdl" {
			foundMainPath = true
		}
		if arg == "--workflow-outputs-key" && i+1 < len(prepared.RunnerSpec.Args) && prepared.RunnerSpec.Args[i+1] == "v1/workflow-runs/run-123/metadata/workflow-outputs.json" {
			foundOutputsKey = true
		}
	}
	if !foundMainPath {
		t.Fatal("runner args missing normalized main workflow path")
	}
	if !foundOutputsKey {
		t.Fatal("runner args missing workflow outputs artifact key")
	}

	if len(prepared.RunnerSpec.Env) != 1 || prepared.RunnerSpec.Env[0].Name != "CONQUEST_WORKFLOW_PARAMS" {
		t.Fatalf("runner env = %+v", prepared.RunnerSpec.Env)
	}
}

func TestWDLRunAdapter_PrepareRun_RejectsEmptyDirWorkspace(t *testing.T) {
	adapter := NewWDLRunAdapter(WDLRunAdapterConfig{DefaultRunnerImage: "example.com/cromwell:latest", WorkspaceMode: "emptydir"})

	_, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "WDL",
		WorkflowTypeVersion:      "1.1",
		WorkflowURL:              "https://github.com/acme-org/my-wdl-pipelines",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6","main_workflow_path":"main.wdl"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected workspace validation error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected validation error, got %T", err)
	}
	if ve.Field != "workflow_type" {
		t.Fatalf("field = %q", ve.Field)
	}
}

func TestInferEngine_WDLDefaultsToCromwell(t *testing.T) {
	if got := InferEngine("WDL", ""); got != "cromwell" {
		t.Fatalf("InferEngine(WDL) = %q", got)
	}
}
