package snakemake

import "testing"

func TestResolveRuntime_Default(t *testing.T) {
	rt, err := ResolveRuntime("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != DefaultVersion() {
		t.Fatalf("runtime version = %q", rt.Version)
	}
}

func TestResolveRuntime_RejectsUnsupportedVersion(t *testing.T) {
	if _, err := ResolveRuntime("8.0.0"); err == nil {
		t.Fatal("expected unsupported version error")
	}
}
