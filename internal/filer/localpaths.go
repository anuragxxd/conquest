package filer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anuragxxd/conquest/internal/api/tes"
	wdlengine "github.com/anuragxxd/conquest/internal/engine/wdl"
)

func ManifestNeedsObjectStoreForStageIn(manifest StageManifest) bool {
	for _, input := range manifest.Inputs {
		if input.Content != "" || input.URL == "" {
			continue
		}
		if _, ok := wdlengine.SharedLocalPathFromURL(input.URL); ok {
			continue
		}
		return true
	}
	return false
}

func manifestNeedsObjectStoreForStageOut(manifest StageManifest) bool {
	for _, output := range manifest.Outputs {
		if output.URL == "" {
			continue
		}
		if _, ok := wdlengine.SharedLocalPathFromURL(output.URL); ok {
			continue
		}
		return true
	}
	return false
}

func stageSharedLocalInput(input tes.Input, manifest StageManifest) error {
	sourcePath, ok := wdlengine.SharedLocalPathFromURL(input.URL)
	if !ok {
		return fmt.Errorf("input URL %q is not a trusted shared-local path", input.URL)
	}
	targetPath := resolveTaskPath(manifest.Workdir, input.Path)
	if sameLocalPath(sourcePath, targetPath) {
		_, err := os.Stat(sourcePath)
		return err
	}
	return mirrorLocalPath(sourcePath, targetPath)
}

func stageSharedLocalOutput(output tes.Output, manifest StageManifest) (*OutputFileLog, error) {
	targetPath, ok := wdlengine.SharedLocalPathFromURL(output.URL)
	if !ok {
		return nil, fmt.Errorf("output URL %q is not a trusted shared-local path", output.URL)
	}
	sourcePath := resolveTaskPath(manifest.Workdir, output.Path)
	if !sameLocalPath(sourcePath, targetPath) {
		if err := mirrorLocalPath(sourcePath, targetPath); err != nil {
			return nil, err
		}
	}

	entry := &OutputFileLog{URL: output.URL, Path: sourcePath}
	if fi, err := os.Stat(sourcePath); err == nil && !fi.IsDir() {
		entry.SizeBytes = fmt.Sprintf("%d", fi.Size())
	}
	return entry, nil
}

func resolveTaskPath(workdir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(workdir, value))
}

func sameLocalPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func mirrorLocalPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", source, err)
	}
	if sameLocalPath(source, target) {
		return nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return mirrorSymlink(source, target)
	}
	if info.IsDir() {
		return mirrorDirectory(source, target)
	}
	return mirrorFile(source, target)
}

func mirrorSymlink(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat target %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create target parent %s: %w", target, err)
	}
	linkTarget, err := os.Readlink(source)
	if err != nil {
		return fmt.Errorf("read link %s: %w", source, err)
	}
	if err := os.Symlink(linkTarget, target); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", target, linkTarget, err)
	}
	return nil
}

func mirrorDirectory(source, target string) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return fmt.Errorf("create target directory %s: %w", target, err)
	}

	return filepath.WalkDir(source, func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == source {
			return nil
		}

		rel, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(destination, 0755)
		}

		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return mirrorSymlink(current, destination)
		}
		return mirrorFile(current, destination)
	})
}

func mirrorFile(source, target string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat target %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create target parent %s: %w", target, err)
	}
	if err := os.Link(source, target); err == nil {
		return nil
	}

	src, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source %s: %w", source, err)
	}
	defer src.Close()

	dst, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("create target %s: %w", target, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy %s to %s: %w", source, target, err)
	}
	return nil
}

func hasSharedLocalURL(raw string) bool {
	_, ok := wdlengine.SharedLocalPathFromURL(strings.TrimSpace(raw))
	return ok
}
