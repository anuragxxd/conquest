// Package filer implements the stage-in and stage-out logic for TES tasks.
//
// Stage-in runs as an init container inside the task Job. It downloads
// TES input files from S3 into the local workdir (emptyDir volume).
//
// Stage-out is called by the reconciler after the task completes. It
// uploads TES output files from S3 (if needed) and captures logs.
package filer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/anuragxxd/conquest/internal/api/tes"
	"github.com/anuragxxd/conquest/internal/objstore"
)

// StageManifest describes the inputs and outputs for staging.
type StageManifest struct {
	Inputs  []tes.Input  `json:"inputs,omitempty"`
	Outputs []tes.Output `json:"outputs,omitempty"`
	Workdir string       `json:"workdir"`
}

// StageIn downloads all TES inputs to the local workdir.
func StageIn(ctx context.Context, client objstore.Client, manifest StageManifest, log *slog.Logger) error {
	// Ensure workdir exists.
	if err := os.MkdirAll(manifest.Workdir, 0755); err != nil {
		return fmt.Errorf("create workdir %s: %w", manifest.Workdir, err)
	}

	for i, input := range manifest.Inputs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Handle inline content.
		if input.Content != "" {
			localPath := input.Path
			if !filepath.IsAbs(localPath) {
				localPath = filepath.Join(manifest.Workdir, localPath)
			}
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return fmt.Errorf("input[%d] mkdir: %w", i, err)
			}
			if err := os.WriteFile(localPath, []byte(input.Content), 0644); err != nil {
				return fmt.Errorf("input[%d] write content: %w", i, err)
			}
			log.Info("staged inline content", "path", localPath, "size", len(input.Content))
			continue
		}

		// Download from S3.
		if input.URL == "" {
			log.Warn("input has no url or content, skipping", "index", i)
			continue
		}

		localPath := input.Path
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(manifest.Workdir, localPath)
		}

		var err error
		if input.Type == "DIRECTORY" {
			err = downloadS3DirectoryInput(ctx, client, input.URL, localPath, log)
		} else {
			err = downloadS3Input(ctx, client, input.URL, localPath, log)
		}
		if err != nil {
			return fmt.Errorf("input[%d] download %s: %w", i, input.URL, err)
		}
	}

	// Write the manifest to /stage/manifest.json so it's available for debugging.
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join("/stage", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err == nil {
		_ = os.WriteFile(manifestPath, manifestJSON, 0644)
	}

	log.Info("stage-in complete", "inputs_count", len(manifest.Inputs))
	return nil
}

// downloadS3Input downloads a single file from an S3 URL.
func downloadS3Input(ctx context.Context, client objstore.Client, s3URL, localPath string, log *slog.Logger) error {
	bucket, key, err := parseS3URL(s3URL)
	if err != nil {
		return err
	}
	return downloadS3Object(ctx, client, bucket, key, s3URL, localPath, log)
}

func downloadS3DirectoryInput(ctx context.Context, client objstore.Client, s3URL, localPath string, log *slog.Logger) error {
	bucket, prefix, err := parseS3URL(s3URL)
	if err != nil {
		return err
	}

	keys, err := client.ListPrefix(ctx, bucket, prefix)
	if err != nil {
		return fmt.Errorf("list s3://%s/%s: %w", bucket, prefix, err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("no objects found under s3://%s/%s", bucket, prefix)
	}

	if err := os.MkdirAll(localPath, 0755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", localPath, err)
	}

	downloaded := 0
	trimmedPrefix := strings.TrimSuffix(prefix, "/")
	for _, key := range keys {
		relPath := strings.TrimPrefix(key, trimmedPrefix)
		relPath = strings.TrimPrefix(relPath, "/")
		if relPath == "" {
			continue
		}

		targetPath := filepath.Join(localPath, filepath.FromSlash(relPath))
		if err := downloadS3Object(ctx, client, bucket, key, fmt.Sprintf("s3://%s/%s", bucket, key), targetPath, log); err != nil {
			return err
		}
		downloaded++
	}
	if downloaded == 0 {
		return fmt.Errorf("no files found under s3://%s/%s", bucket, prefix)
	}

	log.Info("downloaded input directory", "url", s3URL, "path", localPath, "files", downloaded)
	return nil
}

func downloadS3Object(ctx context.Context, client objstore.Client, bucket, key, s3URL, localPath string, log *slog.Logger) error {

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", localPath, err)
	}

	reader, err := client.Get(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("get s3://%s/%s: %w", bucket, key, err)
	}
	defer reader.Close()

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", localPath, err)
	}
	defer f.Close()

	n, err := io.Copy(f, reader)
	if err != nil {
		return fmt.Errorf("copy to %s: %w", localPath, err)
	}

	log.Info("downloaded input", "url", s3URL, "path", localPath, "bytes", n)
	return nil
}

// parseS3URL extracts bucket and key from "s3://bucket/key".
func parseS3URL(s3URL string) (bucket, key string, err error) {
	if !strings.HasPrefix(s3URL, "s3://") {
		return "", "", fmt.Errorf("not an s3:// URL: %s", s3URL)
	}
	u, err := url.Parse(s3URL)
	if err != nil {
		return "", "", fmt.Errorf("parse s3 URL %s: %w", s3URL, err)
	}
	bucket = u.Host
	key = strings.TrimPrefix(u.Path, "/")
	if bucket == "" || key == "" {
		return "", "", fmt.Errorf("invalid s3 URL (missing bucket or key): %s", s3URL)
	}
	return bucket, key, nil
}
