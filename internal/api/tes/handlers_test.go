package tes

import (
	"testing"

	"github.com/anuragxxd/conquest/internal/domain"
)

func TestValidateTESTaskWithOptions_AcceptsSharedLocalInputForCromwell(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	req := &Task{
		Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"bash", "-lc", "echo hi"}}},
		Inputs:    []Input{{URL: root + "/cromwell-executions/wf/call-task/execution/script", Path: "/custom/script"}},
	}

	if errMsg := validateTESTaskWithOptions(req, taskValidationOptions{AllowSharedLocalPaths: true}); errMsg != "" {
		t.Fatalf("unexpected validation error: %s", errMsg)
	}
	if errMsg := validateTESTask(req); errMsg == "" {
		t.Fatal("expected public TES validation to reject shared local input URLs")
	}
}

func TestEnsureInternalRunTaskDefaults_InjectsSharedWorkspaceTag(t *testing.T) {
	req := &Task{Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"bash", "-lc", "echo hi"}}}}
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "cromwell"}

	ensureInternalRunTaskDefaults(req, run)

	if got := req.Tags["conquest_workspace_scope"]; got != "shared" {
		t.Fatalf("workspace scope tag = %q", got)
	}
}
