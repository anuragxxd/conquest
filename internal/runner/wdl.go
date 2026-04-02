package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	wdlengine "github.com/anuragxxd/conquest/internal/engine/wdl"
	"github.com/anuragxxd/conquest/internal/objstore"
)

var wdlImportPattern = regexp.MustCompile(`(?m)^\s*import\s+"([^"]+)"`)

type WDLConfig struct {
	RunID                string
	RepoURL              string
	Revision             string
	MainWorkflowPath     string
	OutputDirPrefix      string
	RunLogKey            string
	WorkflowOutputsKey   string
	EngineMetadataKey    string
	InternalTESBase      string
	S3Endpoint           string
	S3Bucket             string
	S3Region             string
	AllowExternalImports bool
}

func RunWDL(ctx context.Context, cfg WDLConfig, log *slog.Logger) error {
	log.Info("starting wdl runner",
		"run_id", cfg.RunID,
		"repo_url", cfg.RepoURL,
		"revision", cfg.Revision,
		"main_workflow_path", cfg.MainWorkflowPath,
	)

	workspaceDir := wdlengine.SharedWorkspaceRoot()
	workflowDir := filepath.Join(workspaceDir, "workflow")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	logFilePath := filepath.Join(workspaceDir, "run.log")
	logFile, err := os.Create(logFilePath)
	if err != nil {
		return fmt.Errorf("create run log: %w", err)
	}
	defer logFile.Close()

	if err := prepareGitCheckout(ctx, cfg.RepoURL, cfg.Revision, workflowDir); err != nil {
		return fmt.Errorf("prepare workflow checkout: %w", err)
	}

	mainWorkflow, err := resolveMainWorkflowPath(workflowDir, cfg.MainWorkflowPath)
	if err != nil {
		return err
	}

	if !cfg.AllowExternalImports {
		if err := validateRepositoryImports(mainWorkflow, workflowDir); err != nil {
			return err
		}
	}

	inputsPath := filepath.Join(workspaceDir, "inputs.json")
	if err := writeWDLInputsFile(inputsPath); err != nil {
		return err
	}

	configPath := filepath.Join(workspaceDir, "miniwdl.cfg")
	if err := os.WriteFile(configPath, []byte(generateMiniwdlConfig(cfg)), 0644); err != nil {
		return fmt.Errorf("write miniwdl config: %w", err)
	}

	multiOut := io.MultiWriter(os.Stdout, logFile)
	multiErr := io.MultiWriter(os.Stderr, logFile)

	runDir := wdlengine.MiniwdlRunDir()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return fmt.Errorf("create miniwdl run dir: %w", err)
	}

	cmdArgs := []string{
		"run",
		mainWorkflow,
		"-i", inputsPath,
		"--cfg", configPath,
		"--dir", runDir,
		"--verbose",
	}
	cmd := exec.CommandContext(ctx, "miniwdl", cmdArgs...)
	cmd.Dir = workflowDir
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr

	// The TES plugin reads these env vars to dispatch tasks to Conquest.
	tesURL := strings.TrimRight(cfg.InternalTESBase, "/") + "/ga4gh/tes/v1"
	cmd.Env = append(os.Environ(),
		"CONQUEST_INTERNAL_TES_URL="+tesURL,
		"CONQUEST_RUN_ID="+cfg.RunID,
		// Force copy-input-files so that files are physically on the PVC at
		// the paths the command string references.
		"MINIWDL__FILE_IO__COPY_INPUT_FILES=true",
		// Disable the download cache to avoid permission conflicts across
		// concurrent runs sharing the same PVC.
		"MINIWDL__DOWNLOAD_CACHE__PUT=false",
		"MINIWDL__DOWNLOAD_CACHE__GET=false",
	)

	log.Info("executing miniwdl", "cmd", "miniwdl "+strings.Join(cmdArgs, " "))
	runErr := cmd.Run()

	// Always attempt to upload the run log, even on failure.
	uploadErr := uploadRunLog(ctx, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.RunID, cfg.RunLogKey, logFilePath, log)
	if uploadErr != nil {
		log.Error("failed to upload run log", "err", uploadErr)
	}

	// Locate the miniwdl output directory (latest run under runDir).
	miniwdlOutDir, metaErr := findMiniwdlOutputDir(runDir)

	client, clientErr := newS3Client(cfg.S3Endpoint, cfg.S3Region)

	// Upload engine metadata (the miniwdl outputs.json) if available.
	if metaErr == nil {
		outputsFile := filepath.Join(miniwdlOutDir, "outputs.json")
		if clientErr != nil {
			if runErr == nil {
				return fmt.Errorf("create S3 client: %w", clientErr)
			}
			log.Error("failed to create S3 client for metadata upload", "err", clientErr)
		} else if _, statErr := os.Stat(outputsFile); statErr == nil {
			if err := uploadFile(ctx, client, cfg.S3Bucket, cfg.EngineMetadataKey, outputsFile); err != nil {
				if runErr == nil {
					return fmt.Errorf("upload engine metadata: %w", err)
				}
				log.Error("failed to upload engine metadata", "err", err)
			}
		}
	}

	if runErr != nil {
		return fmt.Errorf("miniwdl execution failed: %w", runErr)
	}
	if metaErr != nil {
		return fmt.Errorf("locate miniwdl output directory: %w", metaErr)
	}
	if clientErr != nil {
		return fmt.Errorf("create S3 client: %w", clientErr)
	}

	// Read and persist workflow outputs.
	outputsFile := filepath.Join(miniwdlOutDir, "outputs.json")
	outputsJSON, err := readMiniwdlOutputs(outputsFile)
	if err != nil {
		return err
	}

	persistedJSON, err := persistWorkflowOutputs(ctx, client, cfg, outputsJSON, log)
	if err != nil {
		return err
	}
	if len(persistedJSON) > 0 {
		if err := putArtifact(ctx, client, cfg.S3Bucket, cfg.WorkflowOutputsKey, persistedJSON); err != nil {
			return fmt.Errorf("upload workflow outputs artifact: %w", err)
		}
	}

	log.Info("wdl runner completed successfully", "run_id", cfg.RunID)
	return nil
}

// findMiniwdlOutputDir locates the latest miniwdl run directory. miniwdl
// creates a timestamped subdirectory under the --dir path for each run.
func findMiniwdlOutputDir(runDir string) (string, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return "", fmt.Errorf("read miniwdl run dir: %w", err)
	}

	// miniwdl creates directories with a timestamp prefix, so sorting in
	// reverse lexicographic order gives us the most recent run first.
	var latest string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if latest == "" || name > latest {
			latest = name
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no run subdirectory found in %s", runDir)
	}
	return filepath.Join(runDir, latest), nil
}

// readMiniwdlOutputs reads the outputs.json produced by miniwdl and returns
// the raw JSON bytes. The file maps output names to their typed values.
func readMiniwdlOutputs(outputsFile string) (json.RawMessage, error) {
	content, err := os.ReadFile(outputsFile)
	if err != nil {
		return nil, fmt.Errorf("read miniwdl outputs: %w", err)
	}
	// Validate that it parses as JSON.
	var raw json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, fmt.Errorf("parse miniwdl outputs: %w", err)
	}
	return raw, nil
}

func resolveMainWorkflowPath(workflowDir, relativePath string) (string, error) {
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return "", fmt.Errorf("main workflow path is required")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("main workflow path must be repository-relative")
	}
	cleaned := path.Clean(relativePath)
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("main workflow path must stay within the repository root")
	}
	if !strings.HasSuffix(strings.ToLower(cleaned), ".wdl") {
		return "", fmt.Errorf("main workflow path must point to a .wdl file")
	}

	resolved := filepath.Join(workflowDir, filepath.FromSlash(cleaned))
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve main workflow path %q: %w", relativePath, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("main workflow path %q is a directory", relativePath)
	}
	return resolved, nil
}

func validateRepositoryImports(mainWorkflowPath, workflowRoot string) error {
	seen := map[string]struct{}{}
	var walk func(string) error
	walk = func(absPath string) error {
		rel, err := filepath.Rel(workflowRoot, absPath)
		if err != nil {
			return fmt.Errorf("resolve relative import path: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if _, ok := seen[rel]; ok {
			return nil
		}
		seen[rel] = struct{}{}

		content, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("read WDL source %s: %w", rel, err)
		}

		matches := wdlImportPattern.FindAllStringSubmatch(string(content), -1)
		for _, match := range matches {
			importTarget := strings.TrimSpace(match[1])
			if importTarget == "" {
				continue
			}
			if parsed, err := url.Parse(importTarget); err == nil && parsed.Scheme != "" {
				return fmt.Errorf("external import %q in %s is not allowed in v1", importTarget, rel)
			}
			if path.IsAbs(importTarget) {
				return fmt.Errorf("absolute import %q in %s is not allowed", importTarget, rel)
			}
			resolvedRel := path.Clean(path.Join(path.Dir(rel), importTarget))
			if resolvedRel == "." || strings.HasPrefix(resolvedRel, "../") || resolvedRel == ".." {
				return fmt.Errorf("import %q in %s escapes the repository root", importTarget, rel)
			}
			resolvedAbs := filepath.Join(workflowRoot, filepath.FromSlash(resolvedRel))
			if _, err := os.Stat(resolvedAbs); err != nil {
				return fmt.Errorf("import %q in %s: %w", importTarget, rel, err)
			}
			if err := walk(resolvedAbs); err != nil {
				return err
			}
		}

		return nil
	}

	return walk(mainWorkflowPath)
}

func writeWDLInputsFile(destination string) error {
	var workflowParams map[string]any
	if raw := strings.TrimSpace(os.Getenv("CONQUEST_WORKFLOW_PARAMS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &workflowParams); err != nil {
			return fmt.Errorf("parse workflow params: %w", err)
		}
	}
	if workflowParams == nil {
		workflowParams = map[string]any{}
	}

	content, err := json.MarshalIndent(workflowParams, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workflow params: %w", err)
	}
	if err := os.WriteFile(destination, content, 0644); err != nil {
		return fmt.Errorf("write inputs file: %w", err)
	}
	return nil
}

func generateMiniwdlConfig(cfg WDLConfig) string {
	return `[scheduler]
container_backend = conquest_tes

[file_io]
copy_input_files = true
output_hardlinks = false

[task_runtime]
defaults = {"docker": "ubuntu:22.04"}
command_shell = /bin/bash

[call_cache]
put = false
get = false
`
}

func persistWorkflowOutputs(ctx context.Context, client objstore.Client, cfg WDLConfig, outputs json.RawMessage, log *slog.Logger) ([]byte, error) {
	if len(outputs) == 0 || string(outputs) == "null" {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal(outputs, &value); err != nil {
		return nil, fmt.Errorf("parse workflow outputs: %w", err)
	}

	outputKeyPrefix, err := s3KeyFromURI(cfg.OutputDirPrefix, cfg.S3Bucket)
	if err != nil {
		return nil, err
	}
	cache := map[string]string{}
	transformed, err := persistOutputValue(ctx, client, cfg.S3Bucket, outputKeyPrefix, value, cache, log)
	if err != nil {
		return nil, err
	}

	content, err := json.MarshalIndent(transformed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal workflow outputs: %w", err)
	}
	return content, nil
}

func persistOutputValue(ctx context.Context, client objstore.Client, bucket, outputKeyPrefix string, value any, cache map[string]string, log *slog.Logger) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			persisted, err := persistOutputValue(ctx, client, bucket, outputKeyPrefix, child, cache, log)
			if err != nil {
				return nil, err
			}
			result[key] = persisted
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			persisted, err := persistOutputValue(ctx, client, bucket, outputKeyPrefix, child, cache, log)
			if err != nil {
				return nil, err
			}
			result[i] = persisted
		}
		return result, nil
	case string:
		localPath, ok := wdlengine.SharedLocalPathFromURL(typed)
		if !ok {
			return typed, nil
		}
		uri, err := persistLocalOutputPath(ctx, client, bucket, outputKeyPrefix, localPath, cache, log)
		if err != nil {
			return nil, err
		}
		return uri, nil
	default:
		return typed, nil
	}
}

func persistLocalOutputPath(ctx context.Context, client objstore.Client, bucket, outputKeyPrefix, localPath string, cache map[string]string, log *slog.Logger) (string, error) {
	if uri, ok := cache[localPath]; ok {
		return uri, nil
	}

	fi, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat workflow output %s: %w", localPath, err)
	}

	root := wdlengine.SharedWorkspaceRoot()
	relPath, err := filepath.Rel(root, localPath)
	if err != nil {
		return "", fmt.Errorf("resolve workflow output %s: %w", localPath, err)
	}
	relPath = filepath.ToSlash(relPath)
	if relPath == "." || strings.HasPrefix(relPath, "../") {
		return "", fmt.Errorf("workflow output %s is outside the shared workspace", localPath)
	}

	key := strings.TrimSuffix(outputKeyPrefix, "/") + "/files/" + relPath
	uri := fmt.Sprintf("s3://%s/%s", bucket, key)
	if fi.IsDir() {
		if err := uploadDirectory(ctx, client, bucket, key, localPath, nil); err != nil {
			return "", fmt.Errorf("upload workflow output directory %s: %w", localPath, err)
		}
		uri += "/"
		log.Info("persisted workflow output directory", "path", localPath, "uri", uri)
	} else {
		if err := uploadFile(ctx, client, bucket, key, localPath); err != nil {
			return "", fmt.Errorf("upload workflow output file %s: %w", localPath, err)
		}
		log.Info("persisted workflow output file", "path", localPath, "uri", uri)
	}

	cache[localPath] = uri
	return uri, nil
}

func putArtifact(ctx context.Context, client objstore.Client, bucket, key string, content []byte) error {
	return client.Put(ctx, bucket, key, bytes.NewReader(content), int64(len(content)))
}
