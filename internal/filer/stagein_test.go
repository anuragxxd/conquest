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
