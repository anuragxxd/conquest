package tes

import (
	"testing"

	"github.com/anuragxxd/conquest/internal/domain"
)

func TestValidateTESTaskWithOptions_AcceptsSharedLocalInputForPVCRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	req := &Task{
		Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"bash", "-lc", "echo hi"}}},
		Inputs:    []Input{{URL: root + "/miniwdl-runs/wf/call-task/execution/script", Path: "/custom/script"}},
	}

	if errMsg := validateTESTaskWithOptions(req, taskValidationOptions{AllowSharedLocalPaths: true}); errMsg != "" {
		t.Fatalf("unexpected validation error: %s", errMsg)
	}
	if errMsg := validateTESTask(req); errMsg == "" {
		t.Fatal("expected public TES validation to reject shared local input URLs")
	}
}

func TestEnsureInternalRunTaskDefaults_InjectsSharedWorkspaceTagForPVCRun(t *testing.T) {
	req := &Task{Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"bash", "-lc", "echo hi"}}}}
	run := &domain.Run{
		RunID:          "run-123",
		WorkflowEngine: "wdl",
		Workspace:      &domain.WorkspaceBinding{Kind: domain.WorkspaceKindPVC, ClaimName: "pvc-run-123"},
	}

	ensureInternalRunTaskDefaults(req, run)

	if got := req.Tags["conquest_workspace_scope"]; got != "shared" {
		t.Fatalf("workspace scope tag = %q", got)
	}
}

func TestEnsureInternalRunTaskDefaults_SkipsNonPVCRun(t *testing.T) {
	req := &Task{Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"bash", "-lc", "echo hi"}}}}
	run := &domain.Run{RunID: "run-456", WorkflowEngine: "nextflow"}

	ensureInternalRunTaskDefaults(req, run)

	if req.Tags != nil {
		t.Fatalf("expected no tags for non-PVC run, got %v", req.Tags)
	}
}
