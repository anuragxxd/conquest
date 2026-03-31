package filer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/anuragxxd/conquest/internal/api/tes"
)

func TestStageInDownloadsDirectoryInputs(t *testing.T) {
	client := &fakeObjstoreClient{
		objects: map[string][]byte{
			"bucket/run/work/task/bbsplit_index/ref/genome/summary.txt": []byte("summary"),
			"bucket/run/work/task/bbsplit_index/ref/genome/1.fa":        []byte(">chr1\nACGT\n"),
		},
	}

	workdir := t.TempDir()
	manifest := StageManifest{
		Workdir: workdir,
		Inputs: []tes.Input{
			{
				URL:  "s3://bucket/run/work/task/bbsplit_index",
				Path: "input_index",
				Type: "DIRECTORY",
			},
		},
	}

	if err := StageIn(context.Background(), client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("StageIn error = %v", err)
	}

	base := filepath.Join(workdir, "input_index", "ref", "genome")
	assertFileContents(t, filepath.Join(base, "summary.txt"), "summary")
	assertFileContents(t, filepath.Join(base, "1.fa"), ">chr1\nACGT\n")
}

func TestStageInLocalizesSharedLocalInputsWithoutObjectStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	sourcePath := filepath.Join(root, "workflow", "imports", "tasks.wdl")
	assertWriteFile(t, sourcePath, "version 1.1\n")

	manifest := StageManifest{
		Workdir: filepath.Join(root, "workspace"),
		Inputs: []tes.Input{{
			URL:  sourcePath,
			Path: filepath.Join(root, "cromwell-executions", "wf", "call-task", "execution", "tasks.wdl"),
		}},
	}

	if ManifestNeedsObjectStoreForStageIn(manifest) {
		t.Fatal("shared-local inputs should not require object storage")
	}
	if err := StageIn(context.Background(), nil, manifest, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("StageIn error = %v", err)
	}

	assertFileContents(t, manifest.Inputs[0].Path, "version 1.1\n")
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("contents of %q = %q, want %q", path, string(data), want)
	}
}

func assertWriteFile(t *testing.T, path, want string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(want), 0644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
