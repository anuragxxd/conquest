package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anuragxxd/conquest/internal/sharedfs"
)

type SnakemakeConfig struct {
	RunID             string
	SnakemakeVersion  string
	RepoURL           string
	Revision          string
	Directory         string
	Snakefile         string
	Targets           []string
	Jobs              int
	DefaultDiskMB     int
	WorkflowBundleURI string
	WorkDirPrefix     string
	OutputDirPrefix   string
	RunLogKey         string
	InternalTESBase   string
	S3Endpoint        string
	S3Bucket          string
	S3Region          string
	TaskImage         string
}

func RunSnakemake(ctx context.Context, cfg SnakemakeConfig, log *slog.Logger) error {
	// Shared workspaces are written by both this runner and task pods that may
	// run as a different UID. Clear the umask so everything we create stays
	// group/other writable. No-op for emptydir runs.
	sharedfs.ApplyUmask(log)

	log.Info("starting snakemake runner",
		"run_id", cfg.RunID,
		"snakemake_version", cfg.SnakemakeVersion,
		"repo_url", cfg.RepoURL,
		"revision", cfg.Revision,
	)

	workspaceDir := "/workspace"
	workflowDir := filepath.Join(workspaceDir, "workflow")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	if err := prepareGitCheckout(ctx, cfg.RepoURL, cfg.Revision, workflowDir); err != nil {
		return fmt.Errorf("prepare workflow checkout: %w", err)
	}

	sourceIndexPath := filepath.Join(workflowDir, ".conquest-source-files.json")
	if err := writeWorkflowSourceIndex(ctx, workflowDir, sourceIndexPath); err != nil {
		return err
	}

	workDir := workflowDir
	if directory := strings.TrimSpace(cfg.Directory); directory != "" {
		workDir = filepath.Join(workflowDir, filepath.FromSlash(directory))
	}
	fi, err := os.Stat(workDir)
	if err != nil {
		return fmt.Errorf("resolve snakemake directory %q: %w", cfg.Directory, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("snakemake directory %q is not a directory", cfg.Directory)
	}

	configFile, err := writeSnakemakeConfigFile(workDir)
	if err != nil {
		return err
	}

	if err := uploadWorkflowBundle(ctx, cfg, workflowDir); err != nil {
		return fmt.Errorf("upload workflow bundle: %w", err)
	}

	logFile, err := os.Create(filepath.Join(workspaceDir, "run.log"))
	if err != nil {
		return fmt.Errorf("create run log: %w", err)
	}
	defer logFile.Close()

	args := buildSnakemakeArgs(cfg, configFile)

	cmd := exec.CommandContext(ctx, "snakemake", args...)
	cmd.Dir = workflowDir
	cmd.Env = append(os.Environ(),
		"CONQUEST_RUN_ID="+cfg.RunID,
		"CONQUEST_TES_URL="+strings.TrimRight(cfg.InternalTESBase, "/")+"/ga4gh/tes",
		"CONQUEST_WORKFLOW_ROOT="+workflowDir,
		"CONQUEST_WORKFLOW_WORKDIR="+workDir,
		"CONQUEST_WORKFLOW_SOURCE_INDEX="+sourceIndexPath,
		"HOME="+workflowDir,
		"XDG_CACHE_HOME="+filepath.Join(workflowDir, ".cache"),
		"CONQUEST_WORKFLOW_BUNDLE_URI="+cfg.WorkflowBundleURI,
		"CONQUEST_SNAKEMAKE_WORKDIR_URI="+cfg.WorkDirPrefix,
		"CONQUEST_OUTPUT_DIR_URI="+cfg.OutputDirPrefix,
		"CONQUEST_S3_ENDPOINT="+cfg.S3Endpoint,
		"CONQUEST_S3_BUCKET="+cfg.S3Bucket,
		"CONQUEST_S3_REGION="+cfg.S3Region,
	)
	if cfg.TaskImage != "" {
		cmd.Env = append(cmd.Env, "CONQUEST_SNAKEMAKE_TASK_IMAGE="+cfg.TaskImage)
	}

	multiOut := io.MultiWriter(os.Stdout, logFile)
	multiErr := io.MultiWriter(os.Stderr, logFile)
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr

	log.Info("executing snakemake", "cmd", "snakemake "+strings.Join(args, " "), "directory", cfg.Directory)

	runErr := cmd.Run()
	uploadErr := uploadRunLog(ctx, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.RunID, cfg.RunLogKey, filepath.Join(workspaceDir, "run.log"), log)
	if uploadErr != nil {
		log.Error("failed to upload run log", "err", uploadErr)
	}
	if runErr != nil {
		return fmt.Errorf("snakemake execution failed: %w", runErr)
	}

	log.Info("snakemake runner completed successfully", "run_id", cfg.RunID)
	return nil
}

// isLocalExecution returns true for Snakemake versions that run all tasks
// locally inside the runner pod (SM7) rather than dispatching via TES (SM9+).
func isLocalExecution(version string) bool {
	return strings.HasPrefix(version, "7.")
}

func buildSnakemakeArgs(cfg SnakemakeConfig, configFile string) []string {
	var args []string

	if isLocalExecution(cfg.SnakemakeVersion) {
		// Snakemake 7.x: local execution, all tasks run in the runner pod.
		args = []string{"--cores", fmt.Sprintf("%d", cfg.Jobs), "--use-conda", "--show-failed-logs", "--keep-going", "--conda-frontend", "conda"}
	} else {
		// Snakemake 9.x+: dispatch tasks via the Conquest TES executor plugin.
		args = []string{"--executor", "conquest-tes", "--jobs", fmt.Sprintf("%d", cfg.Jobs), "--use-conda", "--show-failed-logs"}
	}

	if cfg.Snakefile != "" {
		args = append(args, "--snakefile", cfg.Snakefile)
	}
	if cfg.Directory != "" {
		args = append(args, "--directory", cfg.Directory)
	}
	if configFile != "" {
		args = append(args, "--configfile", configFile)
	}

	if !isLocalExecution(cfg.SnakemakeVersion) {
		// Default resources are only meaningful for TES dispatch mode.
		defaultResources := []string{"tmpdir=system_tmpdir"}
		if cfg.DefaultDiskMB > 0 {
			defaultResources = append(defaultResources, fmt.Sprintf("disk_mb=%d", cfg.DefaultDiskMB))
		}
		for _, resource := range defaultResources {
			args = append(args, "--default-resources", resource)
		}
	}

	if len(cfg.Targets) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Targets...)
	}

	return args
}

func writeSnakemakeConfigFile(workflowDir string) (string, error) {
	var workflowParams map[string]any
	if raw := os.Getenv("CONQUEST_WORKFLOW_PARAMS"); strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &workflowParams); err != nil {
			return "", fmt.Errorf("parse workflow params: %w", err)
		}
	}
	if len(workflowParams) == 0 {
		return "", nil
	}

	configPath := filepath.Join(workflowDir, "conquest.config.json")
	content, err := json.MarshalIndent(workflowParams, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal workflow params: %w", err)
	}
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		return "", fmt.Errorf("write snakemake config: %w", err)
	}
	return configPath, nil
}

func uploadWorkflowBundle(ctx context.Context, cfg SnakemakeConfig, workflowDir string) error {
	client, err := newS3Client(cfg.S3Endpoint, cfg.S3Region)
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}
	keyPrefix, err := s3KeyFromURI(cfg.WorkflowBundleURI, cfg.S3Bucket)
	if err != nil {
		return err
	}
	return uploadDirectory(ctx, client, cfg.S3Bucket, keyPrefix, workflowDir, map[string]struct{}{".git": {}})
}

func writeWorkflowSourceIndex(ctx context.Context, workflowDir, destination string) error {
	files, err := trackedWorkflowFiles(ctx, workflowDir)
	if err != nil {
		return err
	}
	content, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("marshal workflow source index: %w", err)
	}
	if err := os.WriteFile(destination, content, 0644); err != nil {
		return fmt.Errorf("write workflow source index: %w", err)
	}
	return nil
}

func s3KeyFromURI(uri, bucket string) (string, error) {
	prefix := "s3://" + bucket + "/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("URI %q does not belong to bucket %q", uri, bucket)
	}
	return strings.TrimPrefix(uri, prefix), nil
}
