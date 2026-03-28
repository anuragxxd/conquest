package adapter

import (
	"encoding/json"
	"strings"
	"testing"

	nfengine "github.com/anuragxxd/conquest/internal/engine/nextflow"
)

func TestGenerateNextflowConfig(t *testing.T) {
	runtime, err := nfengine.ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}

	cfg := NextflowRunConfig{
		RunID:           "019d1234-abcd-7890-abcd-1234567890ab",
		Runtime:         runtime,
		RepoURL:         "https://github.com/PacificAnalytics/conquest-acceptance",
		Revision:        "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
		WorkDirURI:      "s3://conquest/v1/workflow-runs/019d1234-abcd-7890-abcd-1234567890ab/work/nextflow/",
		OutputDirURI:    "s3://conquest/v1/workflow-runs/019d1234-abcd-7890-abcd-1234567890ab/outputs/",
		InternalTESBase: "http://conquest.services.svc.cluster.local/internal/runs/019d1234-abcd-7890-abcd-1234567890ab",
		S3Endpoint:      "http://minio.platform.svc.cluster.local:9000",
		S3Region:        "us-east-1",
		WorkflowParams:  json.RawMessage(`{"greeting": "hello"}`),
	}

	gen, err := GenerateNextflowConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Config checks.
	if !strings.Contains(gen.ConfigContent, "process.executor = 'tes'") {
		t.Error("config missing TES executor")
	}
	if !strings.Contains(gen.ConfigContent, "storeDir = null") {
		t.Error("config missing storeDir override for TES")
	}
	if !strings.Contains(gen.ConfigContent, "nf-ga4gh@1.3.0") {
		t.Error("config missing nf-ga4gh plugin")
	}
	if !strings.Contains(gen.ConfigContent, "nf-amazon") {
		t.Error("config missing nf-amazon plugin")
	}
	if !strings.Contains(gen.ConfigContent, "s3PathStyleAccess = true") {
		t.Error("config missing s3PathStyleAccess")
	}
	if !strings.Contains(gen.ConfigContent, "minio.platform.svc.cluster.local:9000") {
		t.Error("config missing MinIO endpoint")
	}

	// TES endpoint must not have trailing slash.
	tesLine := ""
	for _, line := range strings.Split(gen.ConfigContent, "\n") {
		if strings.Contains(line, "tes.endpoint") {
			tesLine = line
			break
		}
	}
	if tesLine == "" {
		t.Fatal("no tes.endpoint line found")
	}
	if strings.HasSuffix(strings.TrimSpace(tesLine), "/'\n") || strings.Contains(tesLine, "v1/'") {
		t.Error("tes.endpoint has trailing slash")
	}
	if !strings.Contains(tesLine, "/internal/runs/") {
		t.Error("tes.endpoint not using internal run-scoped path")
	}
	// Must end with /ga4gh/tes (not /ga4gh/tes/v1 — plugin appends /v1/tasks)
	if strings.Contains(tesLine, "/ga4gh/tes/v1") {
		t.Error("tes.endpoint should NOT include /v1 — nf-ga4gh appends it")
	}

	// CLI args checks.
	args := gen.CLIArgs
	if args[0] != "run" {
		t.Errorf("first arg should be 'run', got %q", args[0])
	}
	if args[1] != cfg.RepoURL {
		t.Errorf("second arg should be repo URL, got %q", args[1])
	}
	foundRevision := false
	foundParamsFile := false
	for i, arg := range args {
		if arg == "-r" && i+1 < len(args) && args[i+1] == cfg.Revision {
			foundRevision = true
		}
		if arg == "-params-file" {
			foundParamsFile = true
		}
	}
	if !foundRevision {
		t.Error("CLI args missing -r <revision>")
	}
	if !foundParamsFile {
		t.Error("CLI args missing -params-file")
	}

	// Params content.
	if gen.ParamsContent == "" {
		t.Error("params content should not be empty")
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(gen.ParamsContent), &params); err != nil {
		t.Fatalf("failed to parse params content: %v", err)
	}
	if got := params["greeting"]; got != "hello" {
		t.Fatalf("params greeting = %v, want hello", got)
	}
	if got := params["outdir"]; got != cfg.OutputDirURI {
		t.Fatalf("params outdir = %v, want %s", got, cfg.OutputDirURI)
	}
}

func TestGenerateNextflowConfig_NoParams(t *testing.T) {
	runtime, err := nfengine.ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}

	cfg := NextflowRunConfig{
		RunID:           "019d1234",
		Runtime:         runtime,
		RepoURL:         "https://github.com/org/repo",
		Revision:        "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
		WorkDirURI:      "s3://bucket/work/",
		InternalTESBase: "http://conquest.svc/internal/runs/019d1234",
		S3Endpoint:      "http://minio:9000",
	}

	gen, err := GenerateNextflowConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gen.ParamsContent != "" {
		t.Error("params content should be empty with no params")
	}

	for _, arg := range gen.CLIArgs {
		if arg == "-params-file" {
			t.Error("CLI args should not include -params-file with no params")
		}
	}
}

func TestGenerateNextflowConfig_WithEntry(t *testing.T) {
	runtime, err := nfengine.ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}

	cfg := NextflowRunConfig{
		RunID:           "019d1234",
		Runtime:         runtime,
		RepoURL:         "https://github.com/org/repo",
		Revision:        "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
		Entry:           "myWorkflow",
		MainScript:      "pipeline.nf",
		Profiles:        []string{"docker", "test"},
		WorkDirURI:      "s3://bucket/work/",
		InternalTESBase: "http://conquest.svc/internal/runs/019d1234",
		S3Endpoint:      "http://minio:9000",
	}

	gen, err := GenerateNextflowConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundEntry := false
	foundMainScript := false
	foundProfiles := false
	for i, arg := range gen.CLIArgs {
		if arg == "-entry" && i+1 < len(gen.CLIArgs) && gen.CLIArgs[i+1] == "myWorkflow" {
			foundEntry = true
		}
		if arg == "-main-script" && i+1 < len(gen.CLIArgs) && gen.CLIArgs[i+1] == "pipeline.nf" {
			foundMainScript = true
		}
		if arg == "-profile" && i+1 < len(gen.CLIArgs) && gen.CLIArgs[i+1] == "docker,test" {
			foundProfiles = true
		}
	}
	if !foundEntry {
		t.Error("CLI args missing -entry")
	}
	if !foundMainScript {
		t.Error("CLI args missing -main-script")
	}
	if !foundProfiles {
		t.Error("CLI args missing -profile docker,test")
	}
}

func TestGenerateNextflowConfig_PreservesExplicitOutdir(t *testing.T) {
	runtime, err := nfengine.ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}

	cfg := NextflowRunConfig{
		RunID:           "019d1234",
		Runtime:         runtime,
		RepoURL:         "https://github.com/org/repo",
		Revision:        "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
		WorkDirURI:      "s3://bucket/work/",
		OutputDirURI:    "s3://bucket/outputs/",
		InternalTESBase: "http://conquest.svc/internal/runs/019d1234",
		S3Endpoint:      "http://minio:9000",
		WorkflowParams:  json.RawMessage(`{"outdir":"s3://custom/outdir/"}`),
	}

	gen, err := GenerateNextflowConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(gen.ParamsContent), &params); err != nil {
		t.Fatalf("failed to parse params content: %v", err)
	}
	if got := params["outdir"]; got != "s3://custom/outdir/" {
		t.Fatalf("params outdir = %v, want explicit custom value", got)
	}
	if _, ok := params["conquest_output_prefix"]; ok {
		t.Fatal("params should not inject conquest_output_prefix")
	}
}
