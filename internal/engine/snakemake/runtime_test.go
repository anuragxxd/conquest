package snakemake

import (
	"strings"
	"testing"
)

func TestDefaultVersion(t *testing.T) {
	if got := DefaultVersion(); got != "9.17.3" {
		t.Fatalf("DefaultVersion() = %q, want %q", got, "9.17.3")
	}
}

func TestSupportedVersions(t *testing.T) {
	versions := SupportedVersions()
	if len(versions) < 2 {
		t.Fatalf("SupportedVersions() returned %d versions, want >= 2", len(versions))
	}

	has := map[string]bool{}
	for _, v := range versions {
		has[v] = true
	}
	for _, want := range []string{"7.32.4", "9.17.3"} {
		if !has[want] {
			t.Errorf("SupportedVersions() missing %q", want)
		}
	}
}

func TestSupportedVersions_ReturnsDefensiveCopy(t *testing.T) {
	v1 := SupportedVersions()
	v1[0] = "MUTATED"
	v2 := SupportedVersions()
	if v2[0] == "MUTATED" {
		t.Fatal("SupportedVersions() returned a mutable reference")
	}
}

func TestResolveRuntime_DefaultVersion(t *testing.T) {
	rt, err := ResolveRuntime("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != "9.17.3" {
		t.Fatalf("Version = %q, want %q", rt.Version, "9.17.3")
	}
}

func TestResolveRuntime_ExplicitDefault(t *testing.T) {
	rt, err := ResolveRuntime("9.17.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != "9.17.3" {
		t.Fatalf("Version = %q, want %q", rt.Version, "9.17.3")
	}
}

func TestResolveRuntime_Snakemake7(t *testing.T) {
	rt, err := ResolveRuntime("7.32.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != "7.32.4" {
		t.Fatalf("Version = %q, want %q", rt.Version, "7.32.4")
	}
	if !rt.LocalExecution {
		t.Fatal("SM7 should have LocalExecution=true")
	}
}

func TestResolveRuntime_SM9NotLocal(t *testing.T) {
	rt, err := ResolveRuntime("9.17.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.LocalExecution {
		t.Fatal("SM9 should have LocalExecution=false")
	}
}

func TestResolveRuntime_UnsupportedVersion(t *testing.T) {
	_, err := ResolveRuntime("6.0.0")
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if !strings.Contains(err.Error(), "unsupported snakemake version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRuntime_TrimsWhitespace(t *testing.T) {
	rt, err := ResolveRuntime("  9.17.3  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Version != "9.17.3" {
		t.Fatalf("Version = %q, want %q", rt.Version, "9.17.3")
	}
}
