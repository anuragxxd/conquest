package taskpolicy

import (
	"testing"
)

// ---------------------------------------------------------------------------
// NormalizeImageRef
// ---------------------------------------------------------------------------

func TestNormalizeImageRef_Docker(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"docker://ubuntu:jammy-20240111", "ubuntu:jammy-20240111"},
		{"docker://ghcr.io/org/image:1.0", "ghcr.io/org/image:1.0"},
		{"docker://quay.io/biocontainers/star:2.7.11b", "quay.io/biocontainers/star:2.7.11b"},
	}
	for _, tt := range tests {
		got, err := NormalizeImageRef(tt.input)
		if err != nil {
			t.Errorf("NormalizeImageRef(%q) error = %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeImageRef(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeImageRef_OCI(t *testing.T) {
	got, err := NormalizeImageRef("oci://ghcr.io/org/image:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghcr.io/org/image:latest" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeImageRef_PlainRef(t *testing.T) {
	tests := []string{
		"ubuntu:24.04",
		"ghcr.io/org/image:1.0",
		"harbor.example.com/team/tool:sha-abc123",
		"python:3.12-slim",
	}
	for _, ref := range tests {
		got, err := NormalizeImageRef(ref)
		if err != nil {
			t.Errorf("NormalizeImageRef(%q) error = %v", ref, err)
			continue
		}
		if got != ref {
			t.Errorf("NormalizeImageRef(%q) = %q, want unchanged", ref, got)
		}
	}
}

func TestNormalizeImageRef_Empty(t *testing.T) {
	got, err := NormalizeImageRef("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestNormalizeImageRef_Whitespace(t *testing.T) {
	got, err := NormalizeImageRef("  docker://ubuntu:jammy  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ubuntu:jammy" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeImageRef_UnsupportedScheme(t *testing.T) {
	_, err := NormalizeImageRef("shub://library/default/image:latest")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestNormalizeImageRef_EmptyAfterScheme(t *testing.T) {
	_, err := NormalizeImageRef("docker://")
	if err == nil {
		t.Fatal("expected error for empty ref after scheme removal")
	}
}

// ---------------------------------------------------------------------------
// Policy.Apply — image normalization
// ---------------------------------------------------------------------------

func TestApply_NormalizesDockerScheme(t *testing.T) {
	p := &Policy{}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "docker://ubuntu:jammy-20240111"}},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if task.Executors[0].Image != "ubuntu:jammy-20240111" {
		t.Fatalf("image = %q, want ubuntu:jammy-20240111", task.Executors[0].Image)
	}
}

func TestApply_PlainImageUnchanged(t *testing.T) {
	p := &Policy{}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if task.Executors[0].Image != "ubuntu:24.04" {
		t.Fatalf("image = %q, want ubuntu:24.04", task.Executors[0].Image)
	}
}

func TestApply_FallsBackToDefaultImage(t *testing.T) {
	p := &Policy{DefaultImage: "conquest-nf-24.10.4-runner:latest"}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: ""}},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if task.Executors[0].Image != "conquest-nf-24.10.4-runner:latest" {
		t.Fatalf("image = %q", task.Executors[0].Image)
	}
}

func TestApply_ErrorsOnEmptyImageNoDefault(t *testing.T) {
	p := &Policy{}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: ""}},
	}
	if err := p.Apply(task); err == nil {
		t.Fatal("expected error for empty image with no default")
	}
}

func TestApply_ErrorsOnUnsupportedScheme(t *testing.T) {
	p := &Policy{}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "shub://library/default/image:latest"}},
	}
	if err := p.Apply(task); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

// ---------------------------------------------------------------------------
// Policy.Apply — resource defaults
// ---------------------------------------------------------------------------

func TestApply_SetsResourceDefaults(t *testing.T) {
	p := &Policy{
		Resources: ResourceLimits{
			DefaultCPUCores: 1,
			DefaultRAMGb:    2,
			DefaultDiskGb:   8,
		},
	}
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if task.Resources == nil {
		t.Fatal("resources should not be nil after apply")
	}
	if task.Resources.CPUCores == nil || *task.Resources.CPUCores != 1 {
		t.Fatalf("cpu = %v, want 1", task.Resources.CPUCores)
	}
	if task.Resources.RAMGb == nil || *task.Resources.RAMGb != 2 {
		t.Fatalf("ram = %v, want 2", task.Resources.RAMGb)
	}
	if task.Resources.DiskGb == nil || *task.Resources.DiskGb != 8 {
		t.Fatalf("disk = %v, want 8", task.Resources.DiskGb)
	}
}

func TestApply_DoesNotOverrideExplicitResources(t *testing.T) {
	p := &Policy{
		Resources: ResourceLimits{
			DefaultCPUCores: 1,
			DefaultRAMGb:    2,
			DefaultDiskGb:   8,
		},
	}
	var cpu int32 = 4
	ram := 16.0
	disk := 32.0
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
		Resources: &ResourceSpec{CPUCores: &cpu, RAMGb: &ram, DiskGb: &disk},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if *task.Resources.CPUCores != 4 {
		t.Fatalf("cpu = %d, want 4", *task.Resources.CPUCores)
	}
	if *task.Resources.RAMGb != 16 {
		t.Fatalf("ram = %f, want 16", *task.Resources.RAMGb)
	}
	if *task.Resources.DiskGb != 32 {
		t.Fatalf("disk = %f, want 32", *task.Resources.DiskGb)
	}
}

// ---------------------------------------------------------------------------
// Policy.Apply — resource clamping
// ---------------------------------------------------------------------------

func TestApply_ClampsResourceCeilings(t *testing.T) {
	p := &Policy{
		Resources: ResourceLimits{
			MaxCPUCores: 8,
			MaxRAMGb:    16,
			MaxDiskGb:   100,
		},
	}
	var cpu int32 = 32
	ram := 64.0
	disk := 500.0
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
		Resources: &ResourceSpec{CPUCores: &cpu, RAMGb: &ram, DiskGb: &disk},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if *task.Resources.CPUCores != 8 {
		t.Fatalf("cpu = %d, want 8 (clamped)", *task.Resources.CPUCores)
	}
	if *task.Resources.RAMGb != 16 {
		t.Fatalf("ram = %f, want 16 (clamped)", *task.Resources.RAMGb)
	}
	if *task.Resources.DiskGb != 100 {
		t.Fatalf("disk = %f, want 100 (clamped)", *task.Resources.DiskGb)
	}
}

func TestApply_RejectsResourceCeilingsWhenConfigured(t *testing.T) {
	p := &Policy{
		Resources: ResourceLimits{
			MaxCPUCores: 8,
			MaxRAMGb:    16,
			MaxDiskGb:   100,
		},
		RejectOnMaxViolation: true,
	}
	var cpu int32 = 32
	ram := 64.0
	disk := 500.0
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
		Resources: &ResourceSpec{CPUCores: &cpu, RAMGb: &ram, DiskGb: &disk},
	}
	if err := p.Apply(task); err == nil {
		t.Fatal("expected max-resource violation to be rejected")
	}
}

func TestApply_AppliesResourceFloors(t *testing.T) {
	p := &Policy{
		Resources: ResourceLimits{
			MinCPUCores: 1,
			MinRAMGb:    0.5,
			MinDiskGb:   1,
		},
	}
	var cpu int32 = 0
	ram := 0.1
	disk := 0.1
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "ubuntu:24.04"}},
		Resources: &ResourceSpec{CPUCores: &cpu, RAMGb: &ram, DiskGb: &disk},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if *task.Resources.CPUCores != 1 {
		t.Fatalf("cpu = %d, want 1 (floor)", *task.Resources.CPUCores)
	}
	if *task.Resources.RAMGb != 0.5 {
		t.Fatalf("ram = %f, want 0.5 (floor)", *task.Resources.RAMGb)
	}
	if *task.Resources.DiskGb != 1 {
		t.Fatalf("disk = %f, want 1 (floor)", *task.Resources.DiskGb)
	}
}

// ---------------------------------------------------------------------------
// Policy.Apply — zero policy (no-op for valid tasks)
// ---------------------------------------------------------------------------

func TestApply_ZeroPolicyPassthroughForNextflow(t *testing.T) {
	// Simulates a Nextflow TES task where the image is already a plain
	// Docker ref and resources are set by the pipeline.
	p := &Policy{}
	var cpu int32 = 4
	ram := 8.0
	disk := 10.0
	task := &TaskSpec{
		Executors: []ExecutorSpec{{Image: "quay.io/biocontainers/fastqc:0.12.1"}},
		Resources: &ResourceSpec{CPUCores: &cpu, RAMGb: &ram, DiskGb: &disk},
	}
	if err := p.Apply(task); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if task.Executors[0].Image != "quay.io/biocontainers/fastqc:0.12.1" {
		t.Fatalf("image changed: %q", task.Executors[0].Image)
	}
	if *task.Resources.CPUCores != 4 || *task.Resources.RAMGb != 8 || *task.Resources.DiskGb != 10 {
		t.Fatal("resources changed unexpectedly")
	}
}
