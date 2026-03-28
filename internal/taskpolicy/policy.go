// Package taskpolicy normalizes and validates TES task requests before
// they are submitted to the K8s backend. All transformations are
// engine-agnostic — they apply equally to Nextflow, Snakemake, and
// standalone TES submissions.
//
// The policy layer sits between TES validation (syntactic) and K8s Job
// creation (structural), owning the "what does the platform allow"
// question.
//
// To avoid import cycles with the tes package, this package defines its
// own minimal types for the data it needs to inspect and modify.
package taskpolicy

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

// ExecutorSpec is the subset of a TES executor that policy cares about.
type ExecutorSpec struct {
	Image string
}

// ResourceSpec is the subset of TES resources that policy cares about.
// Nil pointers mean "not specified by the caller".
type ResourceSpec struct {
	CPUCores *int32
	RAMGb    *float64
	DiskGb   *float64
}

// TaskSpec is the subset of a TES task that policy normalizes.
type TaskSpec struct {
	Executors []ExecutorSpec
	Resources *ResourceSpec
}

// ResourceLimits defines platform-level floors and ceilings for task
// resources. Zero means "no constraint".
type ResourceLimits struct {
	DefaultCPUCores int32   // applied when the request omits cpu
	DefaultRAMGb    float64 // applied when the request omits ram
	DefaultDiskGb   float64 // applied when the request omits disk

	MaxCPUCores int32   // hard ceiling; 0 = unlimited
	MaxRAMGb    float64 // hard ceiling; 0 = unlimited
	MaxDiskGb   float64 // hard ceiling; 0 = unlimited

	MinCPUCores int32   // floor; 0 = no floor
	MinRAMGb    float64 // floor; 0 = no floor
	MinDiskGb   float64 // floor; 0 = no floor
}

// Policy holds the full configuration for task normalization.
type Policy struct {
	Resources            ResourceLimits
	DefaultImage         string // fallback image when none is provided (rare)
	RejectOnMaxViolation bool   // when true, max ceilings are enforced as errors instead of clamps
}

// Apply normalizes a TaskSpec in place. It:
//  1. Normalizes executor image references (strip docker://, validate).
//  2. Applies resource defaults, floors, and ceilings.
//  3. Validates the result.
//
// Returns a non-nil error if the task cannot be made valid. The error is
// safe to return to the caller as an HTTP 400 message.
func (p *Policy) Apply(task *TaskSpec) error {
	if len(task.Executors) == 0 {
		return fmt.Errorf("task has no executors")
	}

	// --- Image normalization ---
	for i := range task.Executors {
		normalized, err := NormalizeImageRef(task.Executors[i].Image)
		if err != nil {
			return fmt.Errorf("executor[%d] image: %w", i, err)
		}
		if normalized == "" && p.DefaultImage != "" {
			normalized = p.DefaultImage
		}
		if normalized == "" {
			return fmt.Errorf("executor[%d] image is required", i)
		}
		task.Executors[i].Image = normalized
	}

	// --- Resource normalization ---
	task.Resources = p.normalizeResources(task.Resources)

	// --- Resource validation ---
	if err := p.validateResources(task.Resources); err != nil {
		return err
	}

	return nil
}

// NormalizeImageRef cleans up a container image reference from any
// workflow engine:
//
//   - "docker://ubuntu:jammy"  -> "ubuntu:jammy"
//   - "oci://ghcr.io/foo:1.0" -> "ghcr.io/foo:1.0"
//   - "ubuntu:jammy"           -> "ubuntu:jammy" (no-op)
//   - ""                       -> "" (caller decides whether to default)
//
// Returns an error for structurally invalid references.
func NormalizeImageRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}

	// Strip known container-engine URI schemes.
	// Snakemake uses "docker://image:tag", Singularity/Apptainer use
	// "docker://", "oras://", "library://", "shub://", etc.
	// We support docker:// and oci:// as valid container-image schemes.
	for _, scheme := range []string{"docker://", "oci://"} {
		if strings.HasPrefix(ref, scheme) {
			ref = strings.TrimPrefix(ref, scheme)
			break
		}
	}

	// Reject refs that still look like URIs with unsupported schemes.
	if strings.Contains(ref, "://") {
		u, err := url.Parse(ref)
		if err == nil && u.Scheme != "" {
			return "", fmt.Errorf("unsupported image scheme %q in %q", u.Scheme, ref)
		}
	}

	// Basic structural validation: must not be empty after stripping,
	// must not contain whitespace.
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("image reference is empty after scheme removal")
	}
	if strings.ContainsAny(ref, " \t\n\r") {
		return "", fmt.Errorf("image reference contains whitespace: %q", ref)
	}

	return ref, nil
}

// normalizeResources applies defaults and clamps to resource requests.
func (p *Policy) normalizeResources(res *ResourceSpec) *ResourceSpec {
	if res == nil {
		res = &ResourceSpec{}
	}

	// Apply defaults for missing fields.
	if (res.CPUCores == nil || *res.CPUCores == 0) && p.Resources.DefaultCPUCores > 0 {
		v := p.Resources.DefaultCPUCores
		res.CPUCores = &v
	}
	if (res.RAMGb == nil || *res.RAMGb == 0) && p.Resources.DefaultRAMGb > 0 {
		v := p.Resources.DefaultRAMGb
		res.RAMGb = &v
	}
	if (res.DiskGb == nil || *res.DiskGb == 0) && p.Resources.DefaultDiskGb > 0 {
		v := p.Resources.DefaultDiskGb
		res.DiskGb = &v
	}

	// Apply floors.
	if res.CPUCores != nil && p.Resources.MinCPUCores > 0 && *res.CPUCores < p.Resources.MinCPUCores {
		v := p.Resources.MinCPUCores
		res.CPUCores = &v
	}
	if res.RAMGb != nil && p.Resources.MinRAMGb > 0 && *res.RAMGb < p.Resources.MinRAMGb {
		v := p.Resources.MinRAMGb
		res.RAMGb = &v
	}
	if res.DiskGb != nil && p.Resources.MinDiskGb > 0 && *res.DiskGb < p.Resources.MinDiskGb {
		v := p.Resources.MinDiskGb
		res.DiskGb = &v
	}

	// Apply ceilings when configured for clamping. Rejecting is handled in
	// validateResources so the caller gets a clear admission error.
	if !p.RejectOnMaxViolation {
		if res.CPUCores != nil && p.Resources.MaxCPUCores > 0 && *res.CPUCores > p.Resources.MaxCPUCores {
			v := p.Resources.MaxCPUCores
			res.CPUCores = &v
		}
		if res.RAMGb != nil && p.Resources.MaxRAMGb > 0 && *res.RAMGb > p.Resources.MaxRAMGb {
			v := p.Resources.MaxRAMGb
			res.RAMGb = &v
		}
		if res.DiskGb != nil && p.Resources.MaxDiskGb > 0 && *res.DiskGb > p.Resources.MaxDiskGb {
			v := p.Resources.MaxDiskGb
			res.DiskGb = &v
		}
	}

	return res
}

// validateResources checks that the (already-normalized) resources are
// within acceptable bounds.
func (p *Policy) validateResources(res *ResourceSpec) error {
	if res == nil {
		return nil
	}
	if res.CPUCores != nil && *res.CPUCores < 0 {
		return fmt.Errorf("cpu_cores must be non-negative (got %d)", *res.CPUCores)
	}
	if res.RAMGb != nil && (*res.RAMGb < 0 || math.IsNaN(*res.RAMGb) || math.IsInf(*res.RAMGb, 0)) {
		return fmt.Errorf("ram_gb must be a finite non-negative number")
	}
	if res.DiskGb != nil && (*res.DiskGb < 0 || math.IsNaN(*res.DiskGb) || math.IsInf(*res.DiskGb, 0)) {
		return fmt.Errorf("disk_gb must be a finite non-negative number")
	}
	if p.RejectOnMaxViolation {
		if res.CPUCores != nil && p.Resources.MaxCPUCores > 0 && *res.CPUCores > p.Resources.MaxCPUCores {
			return fmt.Errorf("cpu_cores %d exceeds platform max %d", *res.CPUCores, p.Resources.MaxCPUCores)
		}
		if res.RAMGb != nil && p.Resources.MaxRAMGb > 0 && *res.RAMGb > p.Resources.MaxRAMGb {
			return fmt.Errorf("ram_gb %.3g exceeds platform max %.3g", *res.RAMGb, p.Resources.MaxRAMGb)
		}
		if res.DiskGb != nil && p.Resources.MaxDiskGb > 0 && *res.DiskGb > p.Resources.MaxDiskGb {
			return fmt.Errorf("disk_gb %.3g exceeds platform max %.3g", *res.DiskGb, p.Resources.MaxDiskGb)
		}
	}
	return nil
}
