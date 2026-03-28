package filer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anuragxxd/conquest/internal/api/tes"
	"github.com/anuragxxd/conquest/internal/objstore"
)

// OutputFileLog records details of an uploaded output file.
// Matches the TES OutputFileLog schema.
type OutputFileLog struct {
	URL       string `json:"url"`
	Path      string `json:"path"`
	SizeBytes string `json:"size_bytes,omitempty"`
}

// StageOut uploads declared TES outputs from the local workdir to S3.
// Returns the output file logs for persistence.
func StageOut(ctx context.Context, client objstore.Client, manifest StageManifest, log *slog.Logger) ([]OutputFileLog, error) {
	var outputLogs []OutputFileLog
	uploadedPaths := make(map[string]struct{})

	for i, output := range manifest.Outputs {
		select {
		case <-ctx.Done():
			return outputLogs, ctx.Err()
		default:
		}

		if output.URL == "" || output.Path == "" {
			log.Warn("output has no url or path, skipping", "index", i)
			continue
		}

		localPath := output.Path
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(manifest.Workdir, localPath)
		}

		// Handle wildcard outputs (path_prefix pattern from nf-ga4gh).
		if output.PathPrefix != "" && (strings.Contains(output.Path, "*") || strings.Contains(output.Path, "?")) {
			logs, err := uploadWildcard(ctx, client, output, manifest.Workdir, log)
			if err != nil {
				log.Warn("wildcard upload failed", "output_index", i, "err", err)
				continue
			}
			outputLogs = append(outputLogs, logs...)
			markUploadedPaths(uploadedPaths, logs)
			continue
		}

		// Handle directory outputs.
		if output.Type == "DIRECTORY" || isDirectory(localPath) {
			logs, err := uploadDirectory(ctx, client, output, localPath, log)
			if err != nil {
				log.Warn("directory upload failed", "output_index", i, "err", err)
				continue
			}
			outputLogs = append(outputLogs, logs...)
			markUploadedPaths(uploadedPaths, logs)
			continue
		}

		// Single file upload.
		uploaded, err := uploadSingleFile(ctx, client, output.URL, localPath, log)
		if err != nil {
			log.Warn("file upload failed", "output_index", i, "path", localPath, "err", err)
			continue
		}
		outputLogs = append(outputLogs, *uploaded)
		uploadedPaths[uploaded.Path] = struct{}{}
	}

	controlLogs, err := uploadControlFiles(ctx, client, manifest, uploadedPaths, log)
	if err != nil {
		log.Warn("control file upload failed", "err", err)
	} else {
		outputLogs = append(outputLogs, controlLogs...)
	}

	log.Info("stage-out complete", "outputs_uploaded", len(outputLogs))
	return outputLogs, nil
}

func markUploadedPaths(uploadedPaths map[string]struct{}, logs []OutputFileLog) {
	for _, entry := range logs {
		uploadedPaths[entry.Path] = struct{}{}
	}
}

func uploadControlFiles(ctx context.Context, client objstore.Client, manifest StageManifest, uploadedPaths map[string]struct{}, log *slog.Logger) ([]OutputFileLog, error) {
	baseURL, ok := controlFilesBaseURL(manifest.Outputs)
	if !ok {
		return nil, nil
	}

	entries, err := os.ReadDir(manifest.Workdir)
	if err != nil {
		return nil, fmt.Errorf("read workdir %s: %w", manifest.Workdir, err)
	}

	var logs []OutputFileLog
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if name != ".exitcode" && !strings.HasPrefix(name, ".command.") {
			continue
		}

		localPath := filepath.Join(manifest.Workdir, name)
		if _, ok := uploadedPaths[localPath]; ok {
			continue
		}

		uploaded, err := uploadSingleFile(ctx, client, baseURL+"/"+name, localPath, log)
		if err != nil {
			log.Warn("control file upload failed", "path", localPath, "err", err)
			continue
		}
		logs = append(logs, *uploaded)
	}

	return logs, nil
}

func controlFilesBaseURL(outputs []tes.Output) (string, bool) {
	for _, output := range outputs {
		name := filepath.Base(output.Path)
		if name != ".exitcode" && !strings.HasPrefix(name, ".command.") {
			continue
		}
		if output.URL == "" {
			continue
		}
		bucket, key, err := parseS3URL(output.URL)
		if err != nil {
			continue
		}
		if idx := strings.LastIndex(key, "/"); idx >= 0 {
			return fmt.Sprintf("s3://%s/%s", bucket, key[:idx]), true
		}
		return fmt.Sprintf("s3://%s", bucket), true
	}

	return "", false
}

func isDirectory(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

func uploadSingleFile(ctx context.Context, client objstore.Client, s3URL, localPath string, log *slog.Logger) (*OutputFileLog, error) {
	bucket, key, err := parseS3URL(s3URL)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", localPath, err)
	}

	if err := client.Put(ctx, bucket, key, f, fi.Size()); err != nil {
		return nil, fmt.Errorf("upload to s3://%s/%s: %w", bucket, key, err)
	}

	log.Info("uploaded output", "url", s3URL, "path", localPath, "bytes", fi.Size())

	return &OutputFileLog{
		URL:       s3URL,
		Path:      localPath,
		SizeBytes: fmt.Sprintf("%d", fi.Size()),
	}, nil
}

func uploadDirectory(ctx context.Context, client objstore.Client, output tes.Output, localPath string, log *slog.Logger) ([]OutputFileLog, error) {
	var logs []OutputFileLog

	bucket, baseKey, err := parseS3URL(output.URL)
	if err != nil {
		return nil, err
	}

	err = filepath.WalkDir(localPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(localPath, path)
		key := baseKey + "/" + relPath

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		if err := client.Put(ctx, bucket, key, f, fi.Size()); err != nil {
			return fmt.Errorf("upload %s: %w", key, err)
		}

		log.Info("uploaded dir entry", "key", key, "bytes", fi.Size())
		logs = append(logs, OutputFileLog{
			URL:       fmt.Sprintf("s3://%s/%s", bucket, key),
			Path:      path,
			SizeBytes: fmt.Sprintf("%d", fi.Size()),
		})
		return nil
	})

	return logs, err
}

func uploadWildcard(ctx context.Context, client objstore.Client, output tes.Output, workdir string, log *slog.Logger) ([]OutputFileLog, error) {
	var logs []OutputFileLog

	// The path is a glob pattern relative to path_prefix.
	pattern := output.Path
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(workdir, pattern)
	}

	patterns, err := expandGlobPatterns(pattern)
	if err != nil {
		return nil, fmt.Errorf("expand glob %s: %w", pattern, err)
	}

	matchSet := make(map[string]struct{})
	var matches []string
	for _, expanded := range patterns {
		expandedMatches, err := filepath.Glob(expanded)
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", expanded, err)
		}
		for _, match := range expandedMatches {
			if _, seen := matchSet[match]; seen {
				continue
			}
			matchSet[match] = struct{}{}
			matches = append(matches, match)
		}
	}
	sort.Strings(matches)

	bucket, baseKey, err := parseS3URL(output.URL)
	if err != nil {
		return nil, err
	}

	prefix := output.PathPrefix
	if prefix == "" {
		prefix = workdir
	}

	for _, match := range matches {
		fi, err := os.Stat(match)
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(prefix, match)
		key := baseKey
		if !strings.HasSuffix(key, "/") {
			key += "/"
		}
		key += relPath

		if fi.IsDir() {
			dirLogs, err := uploadDirectory(ctx, client, tes.Output{URL: fmt.Sprintf("s3://%s/%s", bucket, key)}, match, log)
			if err != nil {
				log.Warn("wildcard: directory upload failed", "key", key, "err", err)
				continue
			}
			logs = append(logs, dirLogs...)
			continue
		}

		f, err := os.Open(match)
		if err != nil {
			log.Warn("wildcard: cannot open", "path", match, "err", err)
			continue
		}

		if err := client.Put(ctx, bucket, key, f, fi.Size()); err != nil {
			f.Close()
			log.Warn("wildcard: upload failed", "key", key, "err", err)
			continue
		}
		f.Close()

		log.Info("uploaded wildcard match", "key", key, "bytes", fi.Size())
		logs = append(logs, OutputFileLog{
			URL:       fmt.Sprintf("s3://%s/%s", bucket, key),
			Path:      match,
			SizeBytes: fmt.Sprintf("%d", fi.Size()),
		})
	}

	return logs, nil
}

func expandGlobPatterns(pattern string) ([]string, error) {
	start, end, ok, err := findBraceGroup(pattern)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{pattern}, nil
	}

	prefix := pattern[:start]
	group := pattern[start+1 : end]
	suffix := pattern[end+1:]

	parts, err := splitBraceGroup(group)
	if err != nil {
		return nil, err
	}

	var expanded []string
	for _, part := range parts {
		nested, err := expandGlobPatterns(prefix + part + suffix)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, nested...)
	}

	return expanded, nil
}

func findBraceGroup(pattern string) (start, end int, ok bool, err error) {
	start = -1
	depth := 0
	escaped := false
	inClass := false

	for i, r := range pattern {
		if escaped {
			escaped = false
			continue
		}

		switch r {
		case '\\':
			escaped = true
		case '[':
			if depth == 0 {
				inClass = true
			}
		case ']':
			if depth == 0 {
				inClass = false
			}
		case '{':
			if inClass {
				continue
			}
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if inClass || depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				return start, i, true, nil
			}
		}
	}

	if depth != 0 {
		return 0, 0, false, fmt.Errorf("unbalanced braces in %q", pattern)
	}

	return 0, 0, false, nil
}

func splitBraceGroup(group string) ([]string, error) {
	var parts []string
	var current strings.Builder
	depth := 0
	escaped := false
	inClass := false

	for _, r := range group {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch r {
		case '\\':
			current.WriteRune(r)
			escaped = true
		case '[':
			if depth == 0 {
				inClass = true
			}
			current.WriteRune(r)
		case ']':
			if depth == 0 {
				inClass = false
			}
			current.WriteRune(r)
		case '{':
			if !inClass {
				depth++
			}
			current.WriteRune(r)
		case '}':
			if !inClass {
				if depth == 0 {
					return nil, fmt.Errorf("unexpected closing brace in %q", group)
				}
				depth--
			}
			current.WriteRune(r)
		case ',':
			if !inClass && depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
			current.WriteRune(r)
		default:
			current.WriteRune(r)
		}
	}

	if escaped {
		return nil, fmt.Errorf("dangling escape in %q", group)
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced nested braces in %q", group)
	}

	parts = append(parts, current.String())
	return parts, nil
}

// MarshalOutputLogs serializes output logs to JSON for persistence.
func MarshalOutputLogs(logs []OutputFileLog) json.RawMessage {
	if len(logs) == 0 {
		return nil
	}
	data, _ := json.Marshal(logs)
	return data
}
