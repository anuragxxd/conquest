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
	"github.com/anuragxxd/conquest/internal/source"
)

// runRunnerNextflow implements the "runner nextflow" subcommand.
// This runs inside the runner K8s Job.
func runRunnerNextflow(log *slog.Logger) error {
	fs := flag.NewFlagSet("runner nextflow", flag.ExitOnError)

	runID := fs.String("run-id", "", "WES run ID")
	nextflowVersion := fs.String("nextflow-version", "", "Resolved Nextflow engine version")
	repoURL := fs.String("repo-url", "", "GitHub HTTPS repository URL")
	revision := fs.String("revision", "", "Git commit SHA")
	entry := fs.String("entry", "", "Nextflow -entry workflow name (optional)")
	mainScript := fs.String("main-script", "", "Nextflow -main-script file (optional)")
	profiles := fs.String("profiles", "", "Comma-separated Nextflow -profile values (optional)")
	workDirPrefix := fs.String("work-dir-prefix", "", "S3 work directory URI")
	outputDirPrefix := fs.String("output-dir-prefix", "", "S3 final outputs directory URI")
	runLogKey := fs.String("run-log-key", "", "Object storage key for the uploaded run log")
	internalTESBase := fs.String("internal-tes-base", "", "Internal TES base URL")
	s3Endpoint := fs.String("s3-endpoint", "", "S3/MinIO endpoint URL")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket name")
	s3Region := fs.String("s3-region", "us-east-1", "S3 region")

	if err := fs.Parse(os.Args[3:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *runID == "" || *repoURL == "" || *revision == "" {
		return fmt.Errorf("--run-id, --repo-url, and --revision are required")
	}
	if *nextflowVersion == "" {
		return fmt.Errorf("--nextflow-version is required")
	}
	if *workDirPrefix == "" || *internalTESBase == "" {
		return fmt.Errorf("--work-dir-prefix and --internal-tes-base are required")
	}
	if *s3Endpoint == "" || *s3Bucket == "" {
		return fmt.Errorf("--s3-endpoint and --s3-bucket are required")
	}

	var normalizedProfiles []string
	if *profiles != "" {
		parsedProfiles := strings.Split(*profiles, ",")
		var err error
		normalizedProfiles, err = source.NormalizeProfiles(parsedProfiles)
		if err != nil {
			return fmt.Errorf("invalid --profiles: %w", err)
		}
	}

	cfg := runner.Config{
		RunID:           *runID,
		NextflowVersion: *nextflowVersion,
		RepoURL:         *repoURL,
		Revision:        *revision,
		Entry:           *entry,
		MainScript:      *mainScript,
		Profiles:        normalizedProfiles,
		WorkDirPrefix:   *workDirPrefix,
		OutputDirPrefix: *outputDirPrefix,
		RunLogKey:       *runLogKey,
		InternalTESBase: *internalTESBase,
		S3Endpoint:      *s3Endpoint,
		S3Bucket:        *s3Bucket,
		S3Region:        *s3Region,
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

	return runner.Run(ctx, cfg, log)
}
