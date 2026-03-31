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
	CromwellVersion      string
	RepoURL              string
	Revision             string
	MainWorkflowPath     string
	OutputDirPrefix      string
	RunLogKey            string
	WorkflowOutputsKey   string
	CromwellMetadataKey  string
	InternalTESBase      string
	S3Endpoint           string
	S3Bucket             string
	S3Region             string
	AllowExternalImports bool
}

type cromwellMetadata struct {
	Status  string          `json:"status"`
	Outputs json.RawMessage `json:"outputs"`
}

func RunWDL(ctx context.Context, cfg WDLConfig, log *slog.Logger) error {
	log.Info("starting wdl runner",
		"run_id", cfg.RunID,
		"cromwell_version", cfg.CromwellVersion,
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

	multiOut := io.MultiWriter(os.Stdout, logFile)
	multiErr := io.MultiWriter(os.Stderr, logFile)
	if err := validateWDLSource(ctx, mainWorkflow, multiOut, multiErr); err != nil {
		return err
	}

	inputsPath := filepath.Join(workspaceDir, "inputs.json")
	if err := writeWDLInputsFile(inputsPath); err != nil {
		return err
	}

	configPath := filepath.Join(workspaceDir, "cromwell.conf")
	if err := os.WriteFile(configPath, []byte(generateCromwellConfig(cfg)), 0644); err != nil {
		return fmt.Errorf("write cromwell config: %w", err)
	}

	optionsPath := filepath.Join(workspaceDir, "workflow-options.json")
	if err := os.WriteFile(optionsPath, []byte(generateCromwellOptions()), 0644); err != nil {
		return fmt.Errorf("write workflow options: %w", err)
	}

	metadataPath := filepath.Join(workspaceDir, "cromwell-metadata.json")
	cmdArgs := []string{
		"-Dconfig.file=" + configPath,
		"-jar", "/opt/cromwell/cromwell.jar",
		"run", mainWorkflow,
		"--inputs", inputsPath,
		"--options", optionsPath,
		"--metadata-output", metadataPath,
	}
	cmd := exec.CommandContext(ctx, "java", cmdArgs...)
	cmd.Dir = workflowDir
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr

	log.Info("executing cromwell", "cmd", "java "+strings.Join(cmdArgs, " "))
	runErr := cmd.Run()

	uploadErr := uploadRunLog(ctx, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.RunID, cfg.RunLogKey, logFilePath, log)
	if uploadErr != nil {
		log.Error("failed to upload run log", "err", uploadErr)
	}

	metadata, metadataErr := readCromwellMetadata(metadataPath)
	client, clientErr := newS3Client(cfg.S3Endpoint, cfg.S3Region)
	if metadataErr == nil {
		if clientErr != nil {
			if runErr == nil {
				return fmt.Errorf("create S3 client: %w", clientErr)
			}
			log.Error("failed to create S3 client for metadata upload", "err", clientErr)
		} else if err := uploadFile(ctx, client, cfg.S3Bucket, cfg.CromwellMetadataKey, metadataPath); err != nil {
			if runErr == nil {
				return fmt.Errorf("upload cromwell metadata: %w", err)
			}
			log.Error("failed to upload cromwell metadata", "err", err)
		}
	}

	if runErr != nil {
		if metadataErr == nil && strings.TrimSpace(metadata.Status) != "" {
			return fmt.Errorf("cromwell execution failed with status %s: %w", metadata.Status, runErr)
		}
		if metadataErr != nil {
			return fmt.Errorf("cromwell execution failed: %w (metadata unavailable: %v)", runErr, metadataErr)
		}
		return fmt.Errorf("cromwell execution failed: %w", runErr)
	}
	if metadataErr != nil {
		return metadataErr
	}
	if metadata.Status != "Succeeded" {
		return fmt.Errorf("cromwell completed with unexpected status %q", metadata.Status)
	}
	if clientErr != nil {
		return fmt.Errorf("create S3 client: %w", clientErr)
	}

	outputsJSON, err := persistWorkflowOutputs(ctx, client, cfg, metadata.Outputs, log)
	if err != nil {
		return err
	}
	if len(outputsJSON) > 0 {
		if err := putArtifact(ctx, client, cfg.S3Bucket, cfg.WorkflowOutputsKey, outputsJSON); err != nil {
			return fmt.Errorf("upload workflow outputs artifact: %w", err)
		}
	}

	log.Info("wdl runner completed successfully", "run_id", cfg.RunID)
	return nil
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

func validateWDLSource(ctx context.Context, mainWorkflowPath string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "java", "-jar", "/opt/cromwell/womtool.jar", "validate", mainWorkflowPath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("womtool validation failed: %w", err)
	}
	return nil
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

func generateCromwellConfig(cfg WDLConfig) string {
	endpoint := strings.TrimRight(cfg.InternalTESBase, "/") + "/ga4gh/tes/v1/tasks"
	root := wdlengine.CromwellExecutionRoot()
	return fmt.Sprintf(`backend {
  default = "TES"
  providers {
    TES {
      actor-factory = "cromwell.backend.impl.tes.TesBackendLifecycleActorFactory"
      config {
        endpoint = %q
        root = %q
        dockerRoot = %q
        output-mode = "root"
        glob-link-command = "ls -L GLOB_PATTERN 2> /dev/null | xargs -I ? ln -s ? GLOB_DIRECTORY"
        use_tes_11_preview_backend_parameters = false
        poll-backoff {
          min = "15 seconds"
          max = "60 seconds"
          multiplier = 1.2
          randomization-factor = 0.1
        }
      }
    }
  }
}
`, endpoint, root, root)
}

func generateCromwellOptions() string {
	options := map[string]any{
		"read_from_cache": false,
		"write_to_cache":  false,
	}
	content, _ := json.MarshalIndent(options, "", "  ")
	return string(content)
}

func readCromwellMetadata(path string) (*cromwellMetadata, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cromwell metadata: %w", err)
	}
	metadata := &cromwellMetadata{}
	if err := json.Unmarshal(content, metadata); err != nil {
		return nil, fmt.Errorf("parse cromwell metadata: %w", err)
	}
	return metadata, nil
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
