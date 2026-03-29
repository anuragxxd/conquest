package adapter

import (
	"encoding/json"
	"testing"

	"github.com/anuragxxd/conquest/internal/storage"
)

func TestNextflowRunAdapter_PrepareRun_DefaultVersionAndImage(t *testing.T) {
	adapter := NewNextflowRunAdapter(NextflowRunAdapterConfig{
		DefaultRunnerImage: "example.com/conquest-nf-24.10.4-runner:latest",
		InternalBaseURL:    "http://conquest.services.svc.cluster.local",
		S3Endpoint:         "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:           "conquest",
		S3Region:           "us-east-1",
	})

	prepared, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:        "NFL",
		WorkflowTypeVersion: "DSL2",
		WorkflowURL:         "https://github.com/nf-core/rnaseq.git",
		WorkflowEngineParameters: json.RawMessage(`{
			"revision": "13328f73915ceda9bb794caee2f662230c5a33b6",
			"profiles": [" docker ", "test"]
		}`),
		WorkflowParams: json.RawMessage(`{"input":"samplesheet.csv"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prepared.WorkflowEngineVersion != "24.10.4" {
		t.Fatalf("engine version = %q", prepared.WorkflowEngineVersion)
	}
	if prepared.RunnerSpec.Image != "example.com/conquest-nf-24.10.4-runner:latest" {
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
	if normalized.WorkflowURL != "https://github.com/nf-core/rnaseq" {
		t.Fatalf("workflow_url = %q", normalized.WorkflowURL)
	}
	if normalized.WorkflowEngine != "nextflow" {
		t.Fatalf("workflow_engine = %q", normalized.WorkflowEngine)
	}
	if normalized.WorkflowEngineVersion != "24.10.4" {
		t.Fatalf("workflow_engine_version = %q", normalized.WorkflowEngineVersion)
	}

	args := prepared.RunnerSpec.Args
	foundProfiles := false
	foundRunLogKey := false
	for i, arg := range args {
		if arg == "--profiles" && i+1 < len(args) && args[i+1] == "docker,test" {
			foundProfiles = true
		}
		if arg == "--run-log-key" && i+1 < len(args) && args[i+1] == "v1/workflow-runs/run-123/logs/run.log" {
			foundRunLogKey = true
		}
	}
	if !foundProfiles {
		t.Fatal("runner args missing normalized profiles")
	}
	if !foundRunLogKey {
		t.Fatal("runner args missing run log key")
	}
}

func TestNextflowRunAdapter_PrepareRun_UsesVersionSpecificImage(t *testing.T) {
	adapter := NewNextflowRunAdapter(NextflowRunAdapterConfig{
		DefaultRunnerImage: "example.com/conquest-nf-24.10.4-runner:latest",
		RunnerImagesByVersion: map[string]string{
			"25.04.3": "example.com/conquest-nf-25.04.3-runner:latest",
		},
		InternalBaseURL: "http://conquest.services.svc.cluster.local",
		S3Endpoint:      "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:        "conquest",
		S3Region:        "us-east-1",
	})

	prepared, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "NFL",
		WorkflowTypeVersion:      "DSL2",
		WorkflowURL:              "https://github.com/nf-core/rnaseq",
		WorkflowEngineVersion:    "25.04.3",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prepared.RunnerSpec.Image != "example.com/conquest-nf-25.04.3-runner:latest" {
		t.Fatalf("runner image = %q", prepared.RunnerSpec.Image)
	}
}

func TestNextflowRunAdapter_PrepareRun_RejectsMissingConfiguredImage(t *testing.T) {
	adapter := NewNextflowRunAdapter(NextflowRunAdapterConfig{
		DefaultRunnerImage: "example.com/conquest-nf-24.10.4-runner:latest",
		InternalBaseURL:    "http://conquest.services.svc.cluster.local",
		S3Endpoint:         "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:           "conquest",
		S3Region:           "us-east-1",
	})

	_, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "NFL",
		WorkflowTypeVersion:      "DSL2",
		WorkflowURL:              "https://github.com/nf-core/rnaseq",
		WorkflowEngineVersion:    "25.04.3",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected missing image error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected validation error, got %T", err)
	}
	if ve.Field != "workflow_engine_version" {
		t.Fatalf("field = %q", ve.Field)
	}
}

func TestNextflowRunAdapter_PrepareRun_RejectsInvalidWorkflowParams(t *testing.T) {
	adapter := NewNextflowRunAdapter(NextflowRunAdapterConfig{
		DefaultRunnerImage: "example.com/conquest-nf-24.10.4-runner:latest",
		InternalBaseURL:    "http://conquest.services.svc.cluster.local",
		S3Endpoint:         "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:           "conquest",
		S3Region:           "us-east-1",
	})

	_, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "NFL",
		WorkflowTypeVersion:      "DSL2",
		WorkflowURL:              "https://github.com/nf-core/rnaseq",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6"}`),
		WorkflowParams:           json.RawMessage(`[]`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected workflow params validation error")
	}
}
