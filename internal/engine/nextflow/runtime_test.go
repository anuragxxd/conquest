package nextflow

import (
	"reflect"
	"testing"
)

func TestResolveRuntime_DefaultsToPlatformVersion(t *testing.T) {
	rt, err := ResolveRuntime("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rt.Version != DefaultVersion() {
		t.Fatalf("version = %q, want %q", rt.Version, DefaultVersion())
	}
}

func TestResolveRuntime_SupportsKnownVersions(t *testing.T) {
	for _, version := range SupportedVersions() {
		rt, err := ResolveRuntime(version)
		if err != nil {
			t.Fatalf("ResolveRuntime(%q) unexpected error: %v", version, err)
		}
		if rt.Version != version {
			t.Fatalf("ResolveRuntime(%q) version = %q", version, rt.Version)
		}
		if len(rt.PlatformPlugins) == 0 {
			t.Fatalf("ResolveRuntime(%q) missing platform plugins", version)
		}
		if len(rt.AllowedPlugins) == 0 {
			t.Fatalf("ResolveRuntime(%q) missing allowed plugins", version)
		}
	}
}

func TestResolveRuntime_RejectsUnsupportedVersion(t *testing.T) {
	if _, err := ResolveRuntime("26.01.0"); err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestResolveRuntime_UsesVersionSpecificPlatformPlugins(t *testing.T) {
	legacy, err := ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	modern, err := ResolveRuntime("25.10.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := legacy.PlatformPlugins[0].Identifier(); got != "nf-ga4gh@1.3.0" {
		t.Fatalf("legacy nf-ga4gh plugin = %q, want nf-ga4gh@1.3.0", got)
	}
	if got := modern.PlatformPlugins[0].Identifier(); got != "nf-ga4gh@1.4.0" {
		t.Fatalf("25.10 nf-ga4gh plugin = %q, want nf-ga4gh@1.4.0", got)
	}
}

func TestSupportedVersions_ReturnsCopy(t *testing.T) {
	got := SupportedVersions()
	got[0] = "mutated"

	if reflect.DeepEqual(got, SupportedVersions()) {
		t.Fatal("SupportedVersions should return a defensive copy")
	}
}

func TestResolveRuntime_ReturnsDefensiveCopies(t *testing.T) {
	rt, err := ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rt.PlatformPlugins[0].Version = "mutated"
	rt.AllowedPlugins[0] = "mutated"

	rt2, err := ResolveRuntime("24.10.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt2.PlatformPlugins[0].Version != "1.3.0" {
		t.Fatalf("platform plugin version = %q", rt2.PlatformPlugins[0].Version)
	}
	if rt2.AllowedPlugins[0] != "nf-ga4gh" {
		t.Fatalf("allowed plugin[0] = %q", rt2.AllowedPlugins[0])
	}
}
