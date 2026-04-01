package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/anuragxxd/conquest/internal/runner"
)

func runRunnerWDL(log *slog.Logger) error {
	fs := flag.NewFlagSet("runner wdl", flag.ExitOnError)

	runID := fs.String("run-id", "", "WES run ID")
	repoURL := fs.String("repo-url", "", "GitHub HTTPS repository URL")
	revision := fs.String("revision", "", "Git commit SHA")
	mainWorkflowPath := fs.String("main-workflow-path", "", "Primary WDL path relative to the repository root")
	outputDirPrefix := fs.String("output-dir-prefix", "", "S3 final outputs directory URI")
	runLogKey := fs.String("run-log-key", "", "Object storage key for the uploaded run log")
	workflowOutputsKey := fs.String("workflow-outputs-key", "", "Object storage key for the persisted workflow outputs JSON")
	engineMetadataKey := fs.String("engine-metadata-key", "", "Object storage key for the persisted engine metadata JSON")
	internalTESBase := fs.String("internal-tes-base", "", "Internal TES base URL")
	s3Endpoint := fs.String("s3-endpoint", "", "S3/MinIO endpoint URL")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket name")
	s3Region := fs.String("s3-region", "us-east-1", "S3 region")
	allowExternalImports := fs.Bool("allow-external-imports", false, "Allow non-repository WDL imports")

	if err := fs.Parse(os.Args[3:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *runID == "" || *repoURL == "" || *revision == "" || *mainWorkflowPath == "" {
		return fmt.Errorf("--run-id, --repo-url, --revision, and --main-workflow-path are required")
	}
	if *outputDirPrefix == "" || *runLogKey == "" || *workflowOutputsKey == "" || *engineMetadataKey == "" {
		return fmt.Errorf("--output-dir-prefix, --run-log-key, --workflow-outputs-key, and --engine-metadata-key are required")
	}
	if *internalTESBase == "" {
		return fmt.Errorf("--internal-tes-base is required")
	}
	if *s3Endpoint == "" || *s3Bucket == "" {
		return fmt.Errorf("--s3-endpoint and --s3-bucket are required")
	}

	cfg := runner.WDLConfig{
		RunID:                *runID,
		RepoURL:              *repoURL,
		Revision:             *revision,
		MainWorkflowPath:     *mainWorkflowPath,
		OutputDirPrefix:      *outputDirPrefix,
		RunLogKey:            *runLogKey,
		WorkflowOutputsKey:   *workflowOutputsKey,
		EngineMetadataKey:    *engineMetadataKey,
		InternalTESBase:      *internalTESBase,
		S3Endpoint:           *s3Endpoint,
		S3Bucket:             *s3Bucket,
		S3Region:             *s3Region,
		AllowExternalImports: *allowExternalImports,
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

	return runner.RunWDL(ctx, cfg, log)
}
