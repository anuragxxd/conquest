package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/anuragxxd/conquest/internal/runner"
)

func runRunnerSnakemake(log *slog.Logger) error {
	fs := flag.NewFlagSet("runner snakemake", flag.ExitOnError)

	runID := fs.String("run-id", "", "WES run ID")
	snakemakeVersion := fs.String("snakemake-version", "", "Resolved Snakemake engine version")
	repoURL := fs.String("repo-url", "", "GitHub HTTPS repository URL")
	revision := fs.String("revision", "", "Git commit SHA")
	directory := fs.String("directory", "", "Workflow-relative working directory")
	snakefile := fs.String("snakefile", "", "Snakemake workflow file relative to the repository root")
	targets := fs.String("targets", "", "Comma-separated Snakemake targets")
	jobs := fs.Int("jobs", 0, "Maximum number of Snakemake jobs to run in parallel")
	defaultDiskMB := fs.Int("default-disk-mb", 0, "Default disk resource in MB for Snakemake jobs")
	workflowBundlePrefix := fs.String("workflow-bundle-prefix", "", "S3 workflow bundle URI")
	workDirPrefix := fs.String("work-dir-prefix", "", "S3 Snakemake work directory URI")
	outputDirPrefix := fs.String("output-dir-prefix", "", "S3 final outputs directory URI")
	runLogKey := fs.String("run-log-key", "", "Object storage key for the uploaded run log")
	internalTESBase := fs.String("internal-tes-base", "", "Internal TES base URL")
	s3Endpoint := fs.String("s3-endpoint", "", "S3/MinIO endpoint URL")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket name")
	s3Region := fs.String("s3-region", "us-east-1", "S3 region")
	taskImage := fs.String("task-image", "", "Image used for compatibility-mode Snakemake TES tasks")

	if err := fs.Parse(os.Args[3:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *runID == "" || *repoURL == "" || *revision == "" {
		return fmt.Errorf("--run-id, --repo-url, and --revision are required")
	}
	if *snakemakeVersion == "" {
		return fmt.Errorf("--snakemake-version is required")
	}
	if *workflowBundlePrefix == "" || *workDirPrefix == "" || *internalTESBase == "" {
		return fmt.Errorf("--workflow-bundle-prefix, --work-dir-prefix, and --internal-tes-base are required")
	}
	if *s3Endpoint == "" || *s3Bucket == "" {
		return fmt.Errorf("--s3-endpoint and --s3-bucket are required")
	}
	if *jobs <= 0 {
		return fmt.Errorf("--jobs must be greater than zero")
	}
	if *defaultDiskMB <= 0 {
		return fmt.Errorf("--default-disk-mb must be greater than zero")
	}

	var normalizedTargets []string
	if *targets != "" {
		parts := strings.Split(*targets, ",")
		normalizedTargets = make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			normalizedTargets = append(normalizedTargets, part)
		}
	}

	if *taskImage == "" {
		*taskImage = os.Getenv("CONQUEST_SNAKEMAKE_TASK_IMAGE")
	}

	cfg := runner.SnakemakeConfig{
		RunID:             *runID,
		SnakemakeVersion:  *snakemakeVersion,
		RepoURL:           *repoURL,
		Revision:          *revision,
		Directory:         strings.TrimSpace(*directory),
		Snakefile:         strings.TrimSpace(*snakefile),
		Targets:           normalizedTargets,
		Jobs:              *jobs,
		DefaultDiskMB:     *defaultDiskMB,
		WorkflowBundleURI: *workflowBundlePrefix,
		WorkDirPrefix:     *workDirPrefix,
		OutputDirPrefix:   *outputDirPrefix,
		RunLogKey:         *runLogKey,
		InternalTESBase:   *internalTESBase,
		S3Endpoint:        *s3Endpoint,
		S3Bucket:          *s3Bucket,
		S3Region:          *s3Region,
		TaskImage:         strings.TrimSpace(*taskImage),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("received signal, cancelling runner")
		cancel()
	}()

	return runner.RunSnakemake(ctx, cfg, log)
}
