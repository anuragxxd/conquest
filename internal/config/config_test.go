package config

import "testing"

func TestLoad_ParsesRunnerImageMapAndPullSecrets(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_NEXTFLOW_RUNNER_IMAGES", `{"25.04.3":"example.com/runner:25.04.3"}`)
	t.Setenv("CONQUEST_CROMWELL_RUNNER_IMAGES", `{"92":"example.com/cromwell:92"}`)
	t.Setenv("CONQUEST_WDL_ALLOW_EXTERNAL_IMPORTS", "true")
	t.Setenv("CONQUEST_TASK_REJECT_ON_MAX_VIOLATION", "true")
	t.Setenv("CONQUEST_IMAGE_PULL_SECRETS", "harbor, mirror ")
	t.Setenv("CONQUEST_RECONCILER_INTERVAL", "11s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.NextflowRunnerImages["25.04.3"]; got != "example.com/runner:25.04.3" {
		t.Fatalf("runner image mapping = %q", got)
	}
	if got := cfg.CromwellRunnerImages["92"]; got != "example.com/cromwell:92" {
		t.Fatalf("cromwell runner image mapping = %q", got)
	}
	if !cfg.WDLAllowExternalImports {
		t.Fatal("expected WDL external import flag to be enabled")
	}
	if !cfg.TaskRejectOnMaxViolation {
		t.Fatal("expected task max-resource policy to reject when configured")
	}
	if len(cfg.ImagePullSecrets) != 2 {
		t.Fatalf("image pull secrets = %v", cfg.ImagePullSecrets)
	}
	if cfg.ReconcilerInterval.String() != "11s" {
		t.Fatalf("reconciler interval = %s", cfg.ReconcilerInterval)
	}
}

func TestLoad_SetsSnakemakeTaskImageDefault(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_SNAKEMAKE_RUNNER_IMAGE", "example.com/conquest-snakemake:latest")
	t.Setenv("CONQUEST_SNAKEMAKE_DEFAULT_DISK_MB", "12288")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SnakemakeTaskImage != cfg.SnakemakeRunnerImage {
		t.Fatalf("task image = %q, runner image = %q", cfg.SnakemakeTaskImage, cfg.SnakemakeRunnerImage)
	}
	if cfg.SnakemakeDefaultDiskMB != 12288 {
		t.Fatalf("snakemake default disk = %d", cfg.SnakemakeDefaultDiskMB)
	}
}

func TestLoad_RejectsInvalidRunnerImageMap(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_NEXTFLOW_RUNNER_IMAGES", `{invalid`)

	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoad_RejectsInvalidCromwellRunnerImageMap(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_CROMWELL_RUNNER_IMAGES", `{invalid`)

	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoad_RejectsMissingSnakemakeRunnerImage(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_SNAKEMAKE_TASK_IMAGE", "example.com/conquest-snakemake:task")

	if _, err := Load(); err == nil {
		t.Fatal("expected snakemake image validation error")
	}
}

func TestLoad_RejectsInvalidSnakemakeDefaultDisk(t *testing.T) {
	t.Setenv("CONQUEST_POSTGRES_DSN", "postgres://example")
	t.Setenv("CONQUEST_S3_BUCKET", "conquest")
	t.Setenv("CONQUEST_SNAKEMAKE_DEFAULT_DISK_MB", "0")

	if _, err := Load(); err == nil {
		t.Fatal("expected snakemake disk validation error")
	}
}
