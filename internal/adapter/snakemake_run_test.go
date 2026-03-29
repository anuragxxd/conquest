package adapter

import (
	"encoding/json"
	"testing"

	"github.com/anuragxxd/conquest/internal/storage"
)

func TestSnakemakeRunAdapter_PrepareRun_DefaultVersionAndImages(t *testing.T) {
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{
		DefaultRunnerImage: "example.com/conquest-smk-9.17.3-runner:latest",
		DefaultTaskImage:   "example.com/conquest-snakemake-task:latest",
		DefaultJobs:        24,
		DefaultDiskMB:      8192,
		InternalBaseURL:    "http://conquest.services.svc.cluster.local",
		S3Endpoint:         "http://minio.platform.svc.cluster.local:9000",
		S3Bucket:           "conquest",
		S3Region:           "us-east-1",
	})

	prepared, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "9.17.3",
		WorkflowURL:              "https://github.com/snakemake-workflows/dna-seq-gatk-variant-calling",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6","snakefile":"workflow/Snakefile","directory":".test","targets":["results/report.html"]}`),
		WorkflowParams:           json.RawMessage(`{"samples":"config/samples.tsv"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prepared.WorkflowEngineVersion != "9.17.3" {
		t.Fatalf("engine version = %q", prepared.WorkflowEngineVersion)
	}
	if prepared.RunnerSpec.Image != "example.com/conquest-smk-9.17.3-runner:latest" {
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
	if normalized.WorkflowEngine != "snakemake" {
		t.Fatalf("workflow_engine = %q", normalized.WorkflowEngine)
	}
	if normalized.WorkflowEngineVersion != "9.17.3" {
		t.Fatalf("workflow_engine_version = %q", normalized.WorkflowEngineVersion)
	}

	args := prepared.RunnerSpec.Args
	foundJobs := false
	foundSnakefile := false
	foundDirectory := false
	foundDefaultDisk := false
	foundTargets := false
	for i, arg := range args {
		if arg == "--jobs" && i+1 < len(args) && args[i+1] == "24" {
			foundJobs = true
		}
		if arg == "--snakefile" && i+1 < len(args) && args[i+1] == "workflow/Snakefile" {
			foundSnakefile = true
		}
		if arg == "--directory" && i+1 < len(args) && args[i+1] == ".test" {
			foundDirectory = true
		}
		if arg == "--default-disk-mb" && i+1 < len(args) && args[i+1] == "8192" {
			foundDefaultDisk = true
		}
		if arg == "--targets" && i+1 < len(args) && args[i+1] == "results/report.html" {
			foundTargets = true
		}
	}
	if !foundJobs {
		t.Fatal("runner args missing jobs")
	}
	if !foundSnakefile {
		t.Fatal("runner args missing snakefile")
	}
	if !foundDirectory {
		t.Fatal("runner args missing directory")
	}
	if !foundDefaultDisk {
		t.Fatal("runner args missing default disk")
	}
	if !foundTargets {
		t.Fatal("runner args missing targets")
	}

	foundTaskImage := false
	for _, env := range prepared.RunnerSpec.Env {
		if env.Name == "CONQUEST_SNAKEMAKE_TASK_IMAGE" && env.Value == "example.com/conquest-snakemake-task:latest" {
			foundTaskImage = true
		}
	}
	if !foundTaskImage {
		t.Fatal("runner env missing snakemake task image")
	}
}

func TestSnakemakeRunAdapter_PrepareRun_UsesVersionSpecificImage(t *testing.T) {
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{
		DefaultRunnerImage: "example.com/runner:sm9",
		DefaultTaskImage:   "example.com/task:sm9",
		RunnerImagesByVersion: map[string]string{
			"7.32.4": "example.com/runner:sm7",
		},
		TaskImagesByVersion: map[string]string{
			"7.32.4": "example.com/task:sm7",
		},
		DefaultJobs:     8,
		DefaultDiskMB:   8192,
		InternalBaseURL: "http://conquest.svc",
		S3Endpoint:      "http://minio:9000",
		S3Bucket:        "conquest",
		S3Region:        "us-east-1",
	})

	prepared, err := adapter.PrepareRun("run-v7", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "7.32.4",
		WorkflowEngineVersion:    "7.32.4",
		WorkflowURL:              "https://github.com/cbg-ethz/V-pipe",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"9765aa7880237490c5862853b4040e4c38fdd514"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prepared.WorkflowEngineVersion != "7.32.4" {
		t.Fatalf("engine version = %q, want %q", prepared.WorkflowEngineVersion, "7.32.4")
	}
	if prepared.RunnerSpec.Image != "example.com/runner:sm7" {
		t.Fatalf("runner image = %q, want %q", prepared.RunnerSpec.Image, "example.com/runner:sm7")
	}

	foundTaskImage := false
	for _, env := range prepared.RunnerSpec.Env {
		if env.Name == "CONQUEST_SNAKEMAKE_TASK_IMAGE" && env.Value == "example.com/task:sm7" {
			foundTaskImage = true
		}
	}
	if !foundTaskImage {
		t.Fatal("runner env should use version-specific task image for SM7")
	}
}

func TestSnakemakeRunAdapter_PrepareRun_RejectsMissingConfiguredImage(t *testing.T) {
	// Only the default SM9 image is configured; requesting SM7 should fail.
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{
		DefaultRunnerImage: "example.com/runner:sm9",
		DefaultJobs:        8,
		DefaultDiskMB:      8192,
	})

	_, err := adapter.PrepareRun("run-v7", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "7.32.4",
		WorkflowEngineVersion:    "7.32.4",
		WorkflowURL:              "https://github.com/cbg-ethz/V-pipe",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"9765aa7880237490c5862853b4040e4c38fdd514"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected validation error for unconfigured SM7")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestSnakemakeRunAdapter_PrepareRun_TaskImageFallsBackToRunner(t *testing.T) {
	// Version-specific runner image set, but no task image — should use runner image.
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{
		DefaultRunnerImage: "example.com/runner:sm9",
		RunnerImagesByVersion: map[string]string{
			"7.32.4": "example.com/runner:sm7",
		},
		// No TaskImagesByVersion for 7.32.4
		DefaultJobs:     8,
		DefaultDiskMB:   8192,
		InternalBaseURL: "http://conquest.svc",
		S3Endpoint:      "http://minio:9000",
		S3Bucket:        "conquest",
		S3Region:        "us-east-1",
	})

	prepared, err := adapter.PrepareRun("run-v7-notask", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "7.32.4",
		WorkflowEngineVersion:    "7.32.4",
		WorkflowURL:              "https://github.com/cbg-ethz/V-pipe",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"9765aa7880237490c5862853b4040e4c38fdd514"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Task image should fall back to runner image
	foundTaskImage := false
	for _, env := range prepared.RunnerSpec.Env {
		if env.Name == "CONQUEST_SNAKEMAKE_TASK_IMAGE" && env.Value == "example.com/runner:sm7" {
			foundTaskImage = true
		}
	}
	if !foundTaskImage {
		t.Fatal("task image should fall back to runner image when not explicitly set")
	}
}

func TestSnakemakeRunAdapter_PrepareRun_RejectsInvalidSnakefile(t *testing.T) {
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{DefaultRunnerImage: "example.com/runner", DefaultJobs: 8, DefaultDiskMB: 8192})

	_, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "9.17.3",
		WorkflowURL:              "https://github.com/snakemake-workflows/dna-seq-gatk-variant-calling",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6","snakefile":"../Snakefile"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected validation error, got %T", err)
	}
}

func TestSnakemakeRunAdapter_PrepareRun_RejectsInvalidDirectory(t *testing.T) {
	adapter := NewSnakemakeRunAdapter(SnakemakeRunAdapterConfig{DefaultRunnerImage: "example.com/runner", DefaultJobs: 8, DefaultDiskMB: 8192})

	_, err := adapter.PrepareRun("run-123", SubmissionRequest{
		WorkflowType:             "SMK",
		WorkflowTypeVersion:      "9.17.3",
		WorkflowURL:              "https://github.com/snakemake-workflows/dna-seq-gatk-variant-calling",
		WorkflowEngineParameters: json.RawMessage(`{"revision":"13328f73915ceda9bb794caee2f662230c5a33b6","directory":"../.test"}`),
	}, storage.Layout{Bucket: "conquest", Version: "v1"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected validation error, got %T", err)
	}
}
