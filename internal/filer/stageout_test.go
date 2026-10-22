package filer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/anuragxxd/conquest/internal/api/tes"
)

type putCall struct {
	bucket string
	key    string
	data   []byte
}

type fakeObjstoreClient struct {
	puts    []putCall
	objects map[string][]byte
	putErr  error
}

func (f *fakeObjstoreClient) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	data, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeObjstoreClient) Put(_ context.Context, bucket, key string, r io.Reader, _ int64) error {
	if f.putErr != nil {
		return f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.puts = append(f.puts, putCall{bucket: bucket, key: key, data: data})
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[bucket+"/"+key] = data
	return nil
}

func (f *fakeObjstoreClient) Delete(context.Context, string, string) error { return nil }

func (f *fakeObjstoreClient) Exists(context.Context, string, string) (bool, error) { return false, nil }

func (f *fakeObjstoreClient) ListPrefix(_ context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	for fullKey := range f.objects {
		if !strings.HasPrefix(fullKey, bucket+"/") {
			continue
		}
		key := strings.TrimPrefix(fullKey, bucket+"/")
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func TestExpandGlobPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{
			name:    "no brace",
			pattern: "/work/results/*.txt",
			want:    []string{"/work/results/*.txt"},
		},
		{
			name:    "single brace group",
			pattern: "/work/results/*.{fna,fna.gz}",
			want: []string{
				"/work/results/*.fna",
				"/work/results/*.fna.gz",
			},
		},
		{
			name:    "nested brace groups",
			pattern: "/work/{results,logs}/*.{log,err}",
			want: []string{
				"/work/logs/*.err",
				"/work/logs/*.log",
				"/work/results/*.err",
				"/work/results/*.log",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandGlobPatterns(tt.pattern)
			if err != nil {
				t.Fatalf("expandGlobPatterns(%q) error = %v", tt.pattern, err)
			}
			sort.Strings(got)
			sort.Strings(tt.want)
			if len(got) != len(tt.want) {
				t.Fatalf("expandGlobPatterns(%q) len = %d, want %d (%v)", tt.pattern, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("expandGlobPatterns(%q)[%d] = %q, want %q", tt.pattern, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStageOutUploadsBraceExpandedWildcardOutputs(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	mustWriteFile(t, filepath.Join(workdir, "results", "sample.fna"), "fna")
	mustWriteFile(t, filepath.Join(workdir, "results", "sample.fna.gz"), "fna.gz")
	mustWriteFile(t, filepath.Join(workdir, "results", "sample.txt"), "txt")

	client := &fakeObjstoreClient{}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			{
				URL:        "s3://bucket/run/work/task",
				Path:       filepath.Join(workdir, "results", "*.{fna,fna.gz}"),
				PathPrefix: workdir,
				Type:       "FILE",
			},
		},
	}

	logs, err := StageOut(ctx, client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}

	if len(logs) != 2 {
		t.Fatalf("StageOut uploaded %d files, want 2", len(logs))
	}

	gotKeys := []string{client.puts[0].key, client.puts[1].key}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"run/work/task/results/sample.fna",
		"run/work/task/results/sample.fna.gz",
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("uploaded key[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
		}
	}
}

func TestStageOutUploadsNextflowControlFiles(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	mustWriteFile(t, filepath.Join(workdir, ".command.out"), "stdout")
	mustWriteFile(t, filepath.Join(workdir, ".command.err"), "stderr")
	mustWriteFile(t, filepath.Join(workdir, ".command.log"), "log")
	mustWriteFile(t, filepath.Join(workdir, ".exitcode"), "0")
	mustWriteFile(t, filepath.Join(workdir, ".command.env"), "ENV=1")
	mustWriteFile(t, filepath.Join(workdir, ".command.trace"), "trace")

	client := &fakeObjstoreClient{}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			{URL: "s3://bucket/run/work/task/.command.out", Path: filepath.Join(workdir, ".command.out")},
			{URL: "s3://bucket/run/work/task/.command.err", Path: filepath.Join(workdir, ".command.err")},
			{URL: "s3://bucket/run/work/task/.command.log", Path: filepath.Join(workdir, ".command.log")},
			{URL: "s3://bucket/run/work/task/.exitcode", Path: filepath.Join(workdir, ".exitcode")},
		},
	}

	logs, err := StageOut(ctx, client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}

	if len(logs) != 6 {
		t.Fatalf("StageOut uploaded %d files, want 6", len(logs))
	}

	gotKeys := make([]string, 0, len(client.puts))
	for _, put := range client.puts {
		gotKeys = append(gotKeys, put.key)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"run/work/task/.command.env",
		"run/work/task/.command.err",
		"run/work/task/.command.log",
		"run/work/task/.command.out",
		"run/work/task/.command.trace",
		"run/work/task/.exitcode",
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("uploaded key[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
		}
	}
}

func TestStageOutInfersDirectoryOutputs(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	mustWriteFile(t, filepath.Join(workdir, "salmon", "index.bin"), "index")
	mustWriteFile(t, filepath.Join(workdir, "salmon", "meta.json"), "{}")

	client := &fakeObjstoreClient{}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			{
				URL:  "s3://bucket/run/work/task/salmon",
				Path: filepath.Join(workdir, "salmon"),
				Type: "FILE",
			},
		},
	}

	logs, err := StageOut(ctx, client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}

	if len(logs) != 2 {
		t.Fatalf("StageOut uploaded %d files, want 2", len(logs))
	}

	gotKeys := []string{client.puts[0].key, client.puts[1].key}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"run/work/task/salmon/index.bin",
		"run/work/task/salmon/meta.json",
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("uploaded key[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
		}
	}
}

func TestStageOutUploadsWildcardDirectoryMatches(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	mustWriteFile(t, filepath.Join(workdir, "multiqc_report_data", "multiqc_sources.txt"), "sources")

	client := &fakeObjstoreClient{}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			{
				URL:        "s3://bucket/run/work/task",
				Path:       filepath.Join(workdir, "*_data"),
				PathPrefix: workdir,
				Type:       "FILE",
			},
		},
	}

	logs, err := StageOut(ctx, client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("StageOut uploaded %d files, want 1", len(logs))
	}

	if got := client.puts[0].key; got != "run/work/task/multiqc_report_data/multiqc_sources.txt" {
		t.Fatalf("uploaded key = %q, want wildcard directory contents", got)
	}
}

func TestStageOutMaterializesSharedLocalOutputsWithoutObjectStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	callRoot := filepath.Join(root, "miniwdl-runs", "wf", "call-task", "execution")
	customWorkdir := filepath.Join(root, "custom-workdir")
	mustWriteFile(t, filepath.Join(callRoot, "script"), "#!/bin/bash\n")
	mustWriteFile(t, filepath.Join(customWorkdir, "stdout"), "hello\n")

	manifest := StageManifest{
		Workdir: customWorkdir,
		Outputs: []tes.Output{{
			URL:  filepath.Join(callRoot, "stdout"),
			Path: filepath.Join(customWorkdir, "stdout"),
		}},
	}

	logs, err := StageOut(ctx, nil, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("StageOut logs = %d, want 1", len(logs))
	}
	assertFileContents(t, filepath.Join(callRoot, "stdout"), "hello\n")
}

func TestStageOutMirrorsRootModeDirectoryIntoExistingExecutionRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("CONQUEST_SHARED_WORKSPACE_ROOT", root)

	callRoot := filepath.Join(root, "miniwdl-runs", "wf", "call-task", "execution")
	customWorkdir := filepath.Join(root, "docker-workdir")
	mustWriteFile(t, filepath.Join(callRoot, "script"), "#!/bin/bash\n")
	mustWriteFile(t, filepath.Join(customWorkdir, "results", "out.txt"), "output\n")

	manifest := StageManifest{
		Workdir: customWorkdir,
		Outputs: []tes.Output{{
			URL:  callRoot,
			Path: customWorkdir,
			Type: "DIRECTORY",
		}},
	}

	logs, err := StageOut(ctx, nil, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("StageOut logs = %d, want 1", len(logs))
	}
	assertFileContents(t, filepath.Join(callRoot, "results", "out.txt"), "output\n")
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func TestStageOutFailsWhenObjectStoreRejectsWrite(t *testing.T) {
	putErr := errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")
	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "out.txt"), "data")
	mustWriteFile(t, filepath.Join(workdir, "results", "a.txt"), "a")
	mustWriteFile(t, filepath.Join(workdir, "glob-1.txt"), "g")

	tests := []struct {
		name   string
		output tes.Output
	}{
		{"file", tes.Output{URL: "s3://bucket/run/out.txt", Path: filepath.Join(workdir, "out.txt")}},
		{"directory", tes.Output{URL: "s3://bucket/run/results", Path: filepath.Join(workdir, "results"), Type: "DIRECTORY"}},
		{"wildcard", tes.Output{URL: "s3://bucket/run/", Path: filepath.Join(workdir, "glob-*.txt"), PathPrefix: workdir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeObjstoreClient{putErr: putErr}
			manifest := StageManifest{Workdir: workdir, Outputs: []tes.Output{tt.output}}

			_, err := StageOut(context.Background(), client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, putErr) {
				t.Fatalf("StageOut error = %v, want it to wrap %v", err, putErr)
			}
		})
	}
}

func TestStageOutFailsWhenControlFileWriteIsRejected(t *testing.T) {
	putErr := errors.New("access denied")
	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, ".exitcode"), "0")

	client := &fakeObjstoreClient{putErr: putErr}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			// Declared but never produced, so only the control file reaches the store.
			{URL: "s3://bucket/run/work/task/.command.out", Path: filepath.Join(workdir, ".command.out")},
		},
	}

	_, err := StageOut(context.Background(), client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, putErr) {
		t.Fatalf("StageOut error = %v, want it to wrap %v", err, putErr)
	}
}

func TestStageOutToleratesMissingDeclaredOutput(t *testing.T) {
	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "present.txt"), "data")

	client := &fakeObjstoreClient{}
	manifest := StageManifest{
		Workdir: workdir,
		Outputs: []tes.Output{
			{URL: "s3://bucket/run/missing.txt", Path: filepath.Join(workdir, "missing.txt")},
			{URL: "s3://bucket/run/present.txt", Path: filepath.Join(workdir, "present.txt")},
		},
	}

	logs, err := StageOut(context.Background(), client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StageOut error = %v, want nil for a missing optional output", err)
	}
	if len(logs) != 1 || logs[0].URL != "s3://bucket/run/present.txt" {
		t.Fatalf("StageOut logs = %+v, want only present.txt", logs)
	}
}
