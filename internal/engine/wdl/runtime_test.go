package wdl

import "testing"

func TestResolveRuntime_DefaultAndSupported(t *testing.T) {
	rt, err := ResolveRuntime("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != DefaultVersion() {
		t.Fatalf("runtime version = %q, want %q", rt.Version, DefaultVersion())
	}

	if err := ValidateWorkflowVersion("1.1"); err != nil {
		t.Fatalf("expected WDL 1.1 to be supported: %v", err)
	}
	if err := ValidateWorkflowVersion("1.2"); err == nil {
		t.Fatal("expected WDL 1.2 to be rejected")
	}
}

func TestResolveRuntime_RejectsUnsupported(t *testing.T) {
	_, err := ResolveRuntime("92")
	if err == nil {
		t.Fatal("expected error for unsupported version '92'")
	}
}

func TestSharedLocalPathFromURL_UsesWorkspaceRootOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	path, ok := SharedLocalPathFromURL(root + "/workflow/main.wdl")
	if !ok {
		t.Fatal("expected absolute shared-local path to be accepted")
	}
	if path != root+"/workflow/main.wdl" {
		t.Fatalf("path = %q", path)
	}

	fileURL, ok := SharedLocalPathFromURL("file://" + root + "/workflow/imports/tasks.wdl")
	if !ok {
		t.Fatal("expected file:// shared-local path to be accepted")
	}
	if fileURL != root+"/workflow/imports/tasks.wdl" {
		t.Fatalf("file URL path = %q", fileURL)
	}

	if _, ok := SharedLocalPathFromURL(t.TempDir() + "/outside.txt"); ok {
		t.Fatal("expected path outside workspace root to be rejected")
	}
}

func TestMiniwdlRunDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	got := MiniwdlRunDir()
	want := root + "/miniwdl-runs"
	if got != want {
		t.Fatalf("MiniwdlRunDir = %q, want %q", got, want)
	}
}
