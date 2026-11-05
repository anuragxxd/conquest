// Package runner implements the Nextflow runner logic that executes inside
// the runner K8s Job. It generates config, runs Nextflow, captures logs,
// and uploads the run log to S3.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/anuragxxd/conquest/internal/adapter"
	nfengine "github.com/anuragxxd/conquest/internal/engine/nextflow"

	"github.com/anuragxxd/conquest/internal/sharedfs"
)

// Config holds the runner invocation parameters, parsed from CLI flags.
type Config struct {
	RunID           string
	NextflowVersion string
	RepoURL         string
	Revision        string
	Entry           string
	MainScript      string
	Profiles        []string
	WorkDirPrefix   string // s3://bucket/v1/workflow-runs/<id>/work/nextflow/
	OutputDirPrefix string // s3://bucket/v1/workflow-runs/<id>/outputs/
	RunLogKey       string
	InternalTESBase string // http://conquest.svc/internal/runs/<id>
	S3Endpoint      string
	S3Bucket        string
	S3Region        string
}

// Run executes the Nextflow runner workflow.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	// Shared workspaces are written by both this runner and task pods that may
	// run as a different UID. Clear the umask so everything we create stays
	// group/other writable. No-op for emptydir runs.
	sharedfs.ApplyUmask(log)

	log.Info("starting nextflow runner",
		"run_id", cfg.RunID,
		"nextflow_version", cfg.NextflowVersion,
		"repo_url", cfg.RepoURL,
		"revision", cfg.Revision,
	)

	// Read workflow params from env if set.
	var workflowParams json.RawMessage
	if v := os.Getenv("CONQUEST_WORKFLOW_PARAMS"); v != "" {
		workflowParams = json.RawMessage(v)
	}

	runtime, err := nfengine.ResolveRuntime(cfg.NextflowVersion)
	if err != nil {
		return fmt.Errorf("resolve nextflow runtime: %w", err)
	}

	// Generate Nextflow config.
	nfCfg := adapter.NextflowRunConfig{
		RunID:           cfg.RunID,
		Runtime:         runtime,
		RepoURL:         cfg.RepoURL,
		Revision:        cfg.Revision,
		Entry:           cfg.Entry,
		MainScript:      cfg.MainScript,
		Profiles:        cfg.Profiles,
		WorkDirURI:      cfg.WorkDirPrefix,
		OutputDirURI:    cfg.OutputDirPrefix,
		InternalTESBase: cfg.InternalTESBase,
		S3Endpoint:      cfg.S3Endpoint,
		S3Region:        cfg.S3Region,
		WorkflowParams:  workflowParams,
	}

	gen, err := adapter.GenerateNextflowConfig(nfCfg)
	if err != nil {
		return fmt.Errorf("generate nextflow config: %w", err)
	}

	// Write config files to /workspace.
	workspaceDir := "/workspace"
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	if err := adapter.WriteConfigFiles(workspaceDir, gen); err != nil {
		return fmt.Errorf("write config files: %w", err)
	}

	log.Info("generated nextflow config",
		"config_file", workspaceDir+"/nextflow.config",
		"cli_args", gen.CLIArgs,
	)

	// Create run log file.
	logFile, err := os.Create(workspaceDir + "/run.log")
	if err != nil {
		return fmt.Errorf("create run log: %w", err)
	}
	defer logFile.Close()

	defer func() {
		uploadCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := uploadRunLog(uploadCtx, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region,
			cfg.RunID, cfg.RunLogKey, workspaceDir+"/run.log", log); err != nil {
			log.Error("failed to upload run log", "err", err)
		}
	}()

	// Execute Nextflow.
	nfCmd := exec.CommandContext(ctx, "nextflow", gen.CLIArgs...)
	nfCmd.Dir = workspaceDir
	nfCmd.Env = append(os.Environ(),
		"NXF_HOME=/.nextflow",
		"NXF_ANSI_LOG=false",
	)

	// Tee stdout/stderr to both the log file and our stdout.
	multiOut := io.MultiWriter(os.Stdout, logFile)
	multiErr := io.MultiWriter(os.Stderr, logFile)
	nfCmd.Stdout = multiOut
	nfCmd.Stderr = multiErr

	log.Info("executing nextflow", "cmd", "nextflow "+strings.Join(gen.CLIArgs, " "))

	nfErr := nfCmd.Run()

	// Upload the run log to S3 regardless of success/failure.
	// uploadErr := uploadRunLog(ctx, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.RunID, cfg.RunLogKey, workspaceDir+"/run.log", log)
	// if uploadErr != nil {
	// 	log.Error("failed to upload run log", "err", uploadErr)
	// }

	if nfErr != nil {
		return fmt.Errorf("nextflow execution failed: %w", nfErr)
	}

	log.Info("nextflow runner completed successfully", "run_id", cfg.RunID)
	return nil
}
