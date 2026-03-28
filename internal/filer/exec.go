// Package filer — exec.go implements the executor wrapper that runs the
// real TES command and uploads outputs before exiting with the original
// exit code.
package filer

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/anuragxxd/conquest/internal/objstore"
)

// ExecAndStageOut runs the user's executor command, uploads declared
// outputs afterward, writes a termination message, then exits with the
// original executor exit code.
//
// This is the core of the in-pod stage-out strategy: the executor
// container runs this wrapper instead of the raw user command.
func ExecAndStageOut(ctx context.Context, manifestJSON string, command []string, log *slog.Logger) (exitCode int) {
	// Run the actual executor command.
	log.Info("filer exec: running executor command", "command", command)

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	execErr := cmd.Run()

	// Capture the exit code.
	exitCode = 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		log.Info("executor exited", "exit_code", exitCode, "err", execErr)
	} else {
		log.Info("executor exited successfully")
	}

	// Parse the manifest for stage-out.
	var manifest StageManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		log.Error("failed to parse stage manifest for stage-out", "err", err)
		writeTerminationMessage(exitCode, "stage_manifest_parse_error", err.Error())
		return exitCode
	}

	if len(manifest.Outputs) == 0 {
		log.Info("no outputs to stage out")
		writeTerminationMessage(exitCode, "ok", "")
		return exitCode
	}

	// Build S3 client.
	endpoint := os.Getenv("CONQUEST_S3_ENDPOINT")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("CONQUEST_S3_REGION")

	if endpoint == "" || accessKey == "" || secretKey == "" {
		log.Error("S3 config incomplete for stage-out")
		writeTerminationMessage(exitCode, "s3_config_error", "missing S3 env vars")
		return exitCode
	}

	useSSL := strings.HasPrefix(endpoint, "https://")
	cleanEndpoint := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")

	client, err := objstore.NewS3Client(objstore.S3Config{
		Endpoint:  cleanEndpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region:    region,
		UseSSL:    useSSL,
	})
	if err != nil {
		log.Error("failed to create S3 client for stage-out", "err", err)
		writeTerminationMessage(exitCode, "s3_client_error", err.Error())
		return exitCode
	}

	// Run stage-out.
	outputLogs, stageErr := StageOut(ctx, client, manifest, log)
	if stageErr != nil {
		log.Error("stage-out failed", "err", stageErr)
		writeTerminationMessage(exitCode, "stage_out_error", stageErr.Error())
		// Don't change exit code — executor exit code is authoritative.
		return exitCode
	}

	// Write output logs to a well-known location so the reconciler can pick them up.
	if len(outputLogs) > 0 {
		logsJSON, _ := json.Marshal(outputLogs)
		outputLogsPath := "/stage/output-logs.json"
		if err := os.MkdirAll("/stage", 0755); err == nil {
			_ = os.WriteFile(outputLogsPath, logsJSON, 0644)
		}
		log.Info("wrote output logs", "path", outputLogsPath, "count", len(outputLogs))
	}

	writeTerminationMessage(exitCode, "ok", "")
	return exitCode
}

// writeTerminationMessage writes a structured termination message
// to /dev/termination-log for K8s to surface.
func writeTerminationMessage(exitCode int, status, errMsg string) {
	msg := map[string]any{
		"exit_code": exitCode,
		"status":    status,
	}
	if errMsg != "" {
		msg["error"] = errMsg
	}
	data, _ := json.Marshal(msg)
	_ = os.WriteFile("/dev/termination-log", data, 0644)
}

// RunExecWrapper is the entry point for the "filer exec" subcommand.
func RunExecWrapper(log *slog.Logger) {
	manifestJSON := os.Getenv("STAGE_MANIFEST")
	if manifestJSON == "" {
		log.Error("STAGE_MANIFEST env var is required")
		os.Exit(1)
	}

	// Everything after "conquest filer exec --" is the real command.
	var command []string
	foundSep := false
	for i, arg := range os.Args {
		if arg == "--" {
			command = os.Args[i+1:]
			foundSep = true
			break
		}
	}
	if !foundSep || len(command) == 0 {
		log.Error("usage: conquest filer exec -- <command> [args...]")
		os.Exit(1)
	}

	ctx := context.Background()
	exitCode := ExecAndStageOut(ctx, manifestJSON, command, log)
	syscall.Exit(exitCode)
}
