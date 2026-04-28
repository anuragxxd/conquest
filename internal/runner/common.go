package runner

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anuragxxd/conquest/internal/objstore"
)

func newS3Client(endpoint, region string) (objstore.Client, error) {
	useSSL := strings.HasPrefix(endpoint, "https://")
	cleanEndpoint := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")

	return objstore.NewS3Client(objstore.S3Config{
		Endpoint:  cleanEndpoint,
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		Region:    region,
		UseSSL:    useSSL,
	})
}

func uploadRunLog(ctx context.Context, endpoint, bucket, region, runID, key, logPath string, log *slog.Logger) error {
	client, err := newS3Client(endpoint, region)
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}

	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat log file: %w", err)
	}

	if err := client.Put(ctx, bucket, key, f, fi.Size()); err != nil {
		return fmt.Errorf("upload run log: %w", err)
	}

	log.Info("uploaded run log", "run_id", runID, "key", key, "size", fi.Size())
	return nil
}

func uploadDirectory(ctx context.Context, client objstore.Client, bucket, keyPrefix, dir string, skip map[string]struct{}) error {
	entries := make([]string, 0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			for prefix := range skip {
				if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if _, ok := skip[rel]; ok {
			return nil
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk directory %s: %w", dir, err)
	}

	sort.Strings(entries)
	for _, rel := range entries {
		localPath := filepath.Join(dir, filepath.FromSlash(rel))
		if err := uploadFile(ctx, client, bucket, strings.TrimSuffix(keyPrefix, "/")+"/"+rel, localPath); err != nil {
			return err
		}
	}

	return nil
}

func uploadFile(ctx context.Context, client objstore.Client, bucket, key, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}

	if err := client.Put(ctx, bucket, key, f, fi.Size()); err != nil {
		return fmt.Errorf("upload %s to %s: %w", localPath, key, err)
	}
	return nil
}

func prepareGitCheckout(ctx context.Context, repoURL, revision, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove existing workflow directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return fmt.Errorf("create workflow parent directory: %w", err)
	}

	// Clone to a temp directory first, then copy to the target. This is
	// required for S3-backed FUSE mounts (objectfs/GeeseFS) where git's
	// internal chmod()/utime() calls fail with EPERM.
	tmpDir, err := os.MkdirTemp("", "conquest-git-*")
	if err != nil {
		return fmt.Errorf("create temp dir for git clone: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cloneDir := filepath.Join(tmpDir, "repo")

	clone := exec.CommandContext(ctx, "git", "clone", repoURL, cloneDir)
	clone.Stdout = io.Discard
	clone.Stderr = os.Stderr
	if err := clone.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w", repoURL, err)
	}

	checkout := exec.CommandContext(ctx, "git", "checkout", revision)
	checkout.Dir = cloneDir
	checkout.Stdout = io.Discard
	checkout.Stderr = os.Stderr
	if err := checkout.Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", revision, err)
	}

	submodules := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--recursive")
	submodules.Dir = cloneDir
	submodules.Stdout = io.Discard
	submodules.Stderr = os.Stderr
	if err := submodules.Run(); err != nil {
		return fmt.Errorf("git submodule update: %w", err)
	}

	// Capture git-tracked file list while .git is on a real filesystem.
	// Write it as .conquest-git-ls-files so callers don't need a .git dir.
	lsFiles := exec.CommandContext(ctx, "git", "ls-files", "--recurse-submodules")
	lsFiles.Dir = cloneDir
	lsOut, err := lsFiles.Output()
	if err == nil && len(lsOut) > 0 {
		_ = os.WriteFile(filepath.Join(cloneDir, ".conquest-git-ls-files"), lsOut, 0644)
	}

	// Remove .git before copying — saves significant S3 transfer for large
	// repos and avoids git lock file issues on FUSE mounts.
	_ = os.RemoveAll(filepath.Join(cloneDir, ".git"))

	// Copy the working tree to the target directory.
	cp := exec.CommandContext(ctx, "cp", "-r", cloneDir+"/.", dir)
	cp.Stdout = io.Discard
	cp.Stderr = os.Stderr
	if err := cp.Run(); err != nil {
		return fmt.Errorf("copy checkout to workspace: %w", err)
	}

	return nil
}

func trackedWorkflowFiles(ctx context.Context, dir string) ([]string, error) {
	// Prefer pre-computed index (written by prepareGitCheckout for FUSE
	// mounts where .git is not copied to the workspace).
	cachedPath := filepath.Join(dir, ".conquest-git-ls-files")
	output, err := os.ReadFile(cachedPath)
	if err != nil {
		// Fall back to live git ls-files when .git is available.
		cmd := exec.CommandContext(ctx, "git", "ls-files", "--recurse-submodules")
		cmd.Dir = dir
		output, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w", err)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, filepath.ToSlash(line))
	}
	sort.Strings(files)
	return files, nil
}
