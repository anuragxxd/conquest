package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/anuragxxd/conquest/internal/adapter"
	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/objstore"
	"github.com/anuragxxd/conquest/internal/repository"
	"github.com/anuragxxd/conquest/internal/storage"
)

func TestSubmitRun_UsesAdapterAndPersistsSubmission(t *testing.T) {
	runs := &runStoreStub{}
	tasks := &taskStoreStub{}
	events := &eventStoreStub{}
	runBackend := &runBackendStub{submission: &backend.RunSubmission{BackendRef: "runner-job-1"}}
	ctrl := NewRunController(
		runs,
		tasks,
		events,
		storage.Layout{Bucket: "conquest", Version: "v1"},
		adapter.NewRegistry(&runAdapterStub{prepared: &adapter.PreparedRun{
			WorkflowEngine:        "nextflow",
			WorkflowEngineVersion: "24.10.4",
			RequestPayload:        json.RawMessage(`{"workflow_url":"https://github.com/org/repo"}`),
			ResolvedWorkflowRef:   "https://github.com/org/repo@abc",
			RunnerSpec: backend.RunnerSpec{
				Image:   "example.com/conquest-nf-24.10.4-runner:latest",
				Command: []string{"/usr/local/bin/conquest"},
				Args:    []string{"runner", "nextflow"},
			},
		}}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ctrl.SetRunBackend(runBackend)

	run, err := ctrl.SubmitRun(context.Background(), &RunRequest{
		WorkflowType:        "NFL",
		WorkflowTypeVersion: "DSL2",
		WorkflowURL:         "https://github.com/org/repo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.State != domain.StateInitializing {
		t.Fatalf("run state = %s", run.State)
	}
	if run.BackendRef != "runner-job-1" {
		t.Fatalf("run backend ref = %q", run.BackendRef)
	}
	if runs.createRun == nil {
		t.Fatal("run was not persisted")
	}
	if runs.backendRef != "runner-job-1" {
		t.Fatalf("persisted backend ref = %q", runs.backendRef)
	}
	if len(runs.stateUpdates) != 1 || runs.stateUpdates[0].to != domain.StateInitializing {
		t.Fatalf("state updates = %+v", runs.stateUpdates)
	}
	if runBackend.spec == nil || runBackend.spec.Image != "example.com/conquest-nf-24.10.4-runner:latest" {
		t.Fatal("run backend did not receive prepared runner spec")
	}
}

func TestSubmitRun_CleansUpWhenPersistSubmittedRunFails(t *testing.T) {
	runs := &runStoreStub{setBackendRefErr: errors.New("boom")}
	ctrl := NewRunController(
		runs,
		&taskStoreStub{},
		&eventStoreStub{},
		storage.Layout{Bucket: "conquest", Version: "v1"},
		adapter.NewRegistry(&runAdapterStub{prepared: &adapter.PreparedRun{
			WorkflowEngine:        "nextflow",
			WorkflowEngineVersion: "24.10.4",
			RequestPayload:        json.RawMessage(`{"workflow_url":"https://github.com/org/repo"}`),
			ResolvedWorkflowRef:   "https://github.com/org/repo@abc",
			RunnerSpec: backend.RunnerSpec{
				Image:   "example.com/conquest-nf-24.10.4-runner:latest",
				Command: []string{"/usr/local/bin/conquest"},
				Args:    []string{"runner", "nextflow"},
			},
		}}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	runBackend := &runBackendStub{submission: &backend.RunSubmission{BackendRef: "runner-job-1"}}
	ctrl.SetRunBackend(runBackend)

	run, err := ctrl.SubmitRun(context.Background(), &RunRequest{
		WorkflowType:        "NFL",
		WorkflowTypeVersion: "DSL2",
		WorkflowURL:         "https://github.com/org/repo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.State != domain.StateSystemError {
		t.Fatalf("run state = %s", run.State)
	}
	if !runBackend.cancelCalled {
		t.Fatal("expected backend cleanup cancel")
	}
	if runs.errorSummary == "" {
		t.Fatal("expected persisted error summary")
	}
}

func TestGetRun_LoadsWorkflowOutputsForWDL(t *testing.T) {
	runs := &runStoreStub{getRun: &domain.Run{RunID: "run-123", WorkflowEngine: "wdl"}}
	ctrl := NewRunController(
		runs,
		&taskStoreStub{},
		&eventStoreStub{},
		storage.Layout{Bucket: "conquest", Version: "v1"},
		adapter.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ctrl.SetArtifactStore(&artifactStoreStub{objects: map[string][]byte{
		"conquest/v1/workflow-runs/run-123/metadata/workflow-outputs.json": []byte(`{"wf.out":"s3://conquest/v1/workflow-runs/run-123/outputs/files/out.txt"}`),
	}})

	result, err := ctrl.GetRun(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Outputs) == "" {
		t.Fatal("expected workflow outputs to be loaded")
	}
}

func newOutputsTestController(run *domain.Run, store *artifactStoreStub) *RunController {
	ctrl := NewRunController(
		&runStoreStub{getRun: run},
		&taskStoreStub{},
		&eventStoreStub{},
		storage.Layout{Bucket: "conquest", Version: "v1"},
		adapter.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ctrl.SetArtifactStore(store)
	return ctrl
}

func outputObjects() map[string][]byte {
	return map[string][]byte{
		"conquest/v1/workflow-runs/run-123/outputs/multiqc/report.html": []byte("<html></html>"),
		"conquest/v1/workflow-runs/run-123/outputs/counts/s 1#.tsv":     []byte("gene\t1\n"),
		"conquest/v1/workflow-runs/run-123/outputs/counts/":             nil, // directory marker
		"conquest/v1/workflow-runs/run-123/outputs/a.txt":               []byte("a"),
		"conquest/v1/workflow-runs/run-123/logs/run.log":                []byte("log"),
		"conquest/v1/workflow-runs/run-1234/outputs/other.txt":          []byte("other run"),
	}
}

func TestGetRun_ListsOutputFilesForNextflowAndSnakemake(t *testing.T) {
	for _, engine := range []string{"nextflow", "snakemake"} {
		t.Run(engine, func(t *testing.T) {
			run := &domain.Run{RunID: "run-123", WorkflowEngine: engine, State: domain.StateComplete}
			ctrl := newOutputsTestController(run, &artifactStoreStub{objects: outputObjects()})

			result, err := ctrl.GetRun(context.Background(), "run-123")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := `{"files":[` +
				`{"path":"a.txt","url":"s3://conquest/v1/workflow-runs/run-123/outputs/a.txt","size":1},` +
				`{"path":"counts/s 1#.tsv","url":"s3://conquest/v1/workflow-runs/run-123/outputs/counts/s 1#.tsv","size":7},` +
				`{"path":"multiqc/report.html","url":"s3://conquest/v1/workflow-runs/run-123/outputs/multiqc/report.html","size":13}` +
				`],"truncated":false}`
			if string(result.Outputs) != want {
				t.Fatalf("outputs =\n%s\nwant\n%s", result.Outputs, want)
			}
		})
	}
}

func TestGetRun_ListsOutputFilesOfRunsThatFailed(t *testing.T) {
	for _, state := range []domain.State{domain.StateExecutorError, domain.StateSystemError, domain.StateCanceled, domain.StatePreempted} {
		run := &domain.Run{RunID: "run-123", WorkflowEngine: "nextflow", State: state}
		ctrl := newOutputsTestController(run, &artifactStoreStub{objects: outputObjects()})

		result, err := ctrl.GetRun(context.Background(), "run-123")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", state, err)
		}
		if !bytes.Contains(result.Outputs, []byte(`"path":"a.txt"`)) {
			t.Fatalf("%s: outputs = %s, want the listed files", state, result.Outputs)
		}
	}
}

func TestGetRun_ListsNoOutputFilesWhileRunActive(t *testing.T) {
	for _, state := range []domain.State{domain.StateQueued, domain.StateInitializing, domain.StateRunning, domain.StatePaused, domain.StateCanceling} {
		store := &artifactStoreStub{objects: outputObjects()}
		run := &domain.Run{RunID: "run-123", WorkflowEngine: "snakemake", State: state}
		ctrl := newOutputsTestController(run, store)

		result, err := ctrl.GetRun(context.Background(), "run-123")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", state, err)
		}
		if result.Outputs != nil || store.listed != 0 {
			t.Fatalf("%s: outputs = %s, listed %d objects; want no listing", state, result.Outputs, store.listed)
		}
	}
}

func TestGetRun_ReportsEmptyOutputDirectory(t *testing.T) {
	run := &domain.Run{RunID: "run-9", WorkflowEngine: "nextflow", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, &artifactStoreStub{objects: outputObjects()})

	result, err := ctrl.GetRun(context.Background(), "run-9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `{"files":[],"truncated":false}`; string(result.Outputs) != want {
		t.Fatalf("outputs = %s, want %s", result.Outputs, want)
	}
}

func TestGetRun_CapsOutputFiles(t *testing.T) {
	store := &artifactStoreStub{objects: outputObjects()}
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "nextflow", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, store)
	ctrl.SetRunOutputsMaxFiles(2)

	result, err := ctrl.GetRun(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var listing OutputListing
	if err := json.Unmarshal(result.Outputs, &listing); err != nil {
		t.Fatalf("parse outputs: %v", err)
	}
	if !listing.Truncated || len(listing.Files) != 2 {
		t.Fatalf("outputs = %s, want 2 files and truncated", result.Outputs)
	}
	if listing.Files[0].Path != "a.txt" || listing.Files[1].Path != "counts/s 1#.tsv" {
		t.Fatalf("outputs = %s, want the first 2 files by path", result.Outputs)
	}
	// In key order, the fixture yields a.txt, counts/ (a directory marker,
	// not a file), counts/s 1#.tsv and multiqc/report.html: listing stops
	// at the first file beyond the cap, the 4th object.
	if store.listed != 4 {
		t.Fatalf("listed %d objects, want 4", store.listed)
	}
}

func TestGetRun_ExactlyMaxOutputFilesIsNotTruncated(t *testing.T) {
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "nextflow", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, &artifactStoreStub{objects: outputObjects()})
	ctrl.SetRunOutputsMaxFiles(3)

	result, err := ctrl.GetRun(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var listing OutputListing
	if err := json.Unmarshal(result.Outputs, &listing); err != nil {
		t.Fatalf("parse outputs: %v", err)
	}
	if listing.Truncated || len(listing.Files) != 3 {
		t.Fatalf("outputs = %s, want 3 files, not truncated", result.Outputs)
	}
}

func TestGetRun_ZeroMaxOutputFilesDisablesListing(t *testing.T) {
	store := &artifactStoreStub{objects: outputObjects()}
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "nextflow", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, store)
	ctrl.SetRunOutputsMaxFiles(0)

	result, err := ctrl.GetRun(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outputs != nil || store.listed != 0 {
		t.Fatalf("outputs = %s, listed %d objects; want no listing", result.Outputs, store.listed)
	}
}

func TestGetRun_OutputListingErrorLeavesOutputsEmpty(t *testing.T) {
	store := &artifactStoreStub{objects: outputObjects(), listErr: errors.New("storage down")}
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "nextflow", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, store)

	result, err := ctrl.GetRun(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outputs != nil {
		t.Fatalf("outputs = %s, want none", result.Outputs)
	}
}

func TestGetRun_DoesNotListOutputFilesForWDL(t *testing.T) {
	store := &artifactStoreStub{objects: outputObjects()}
	run := &domain.Run{RunID: "run-123", WorkflowEngine: "wdl", State: domain.StateComplete}
	ctrl := newOutputsTestController(run, store)

	if _, err := ctrl.GetRun(context.Background(), "run-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.listed != 0 {
		t.Fatalf("listed %d objects for a WDL run, want 0", store.listed)
	}
}

type runAdapterStub struct {
	prepared *adapter.PreparedRun
	err      error
}

func (s *runAdapterStub) Name() string { return "nextflow" }

func (s *runAdapterStub) PrepareRun(string, adapter.SubmissionRequest, storage.Layout) (*adapter.PreparedRun, error) {
	return s.prepared, s.err
}

type runBackendStub struct {
	submission   *backend.RunSubmission
	err          error
	spec         *backend.RunnerSpec
	cancelCalled bool
}

func (s *runBackendStub) SubmitRun(_ context.Context, _ *domain.Run, spec *backend.RunnerSpec) (*backend.RunSubmission, error) {
	s.spec = spec
	return s.submission, s.err
}

func (s *runBackendStub) DeleteRunJobs(context.Context, string) error { return nil }

func (s *runBackendStub) CancelRun(context.Context, *domain.Run) error {
	s.cancelCalled = true
	return nil
}

func (s *runBackendStub) RefreshRun(context.Context, *domain.Run) (*backend.BackendStatus, error) {
	return nil, nil
}

type runStateUpdate struct {
	from domain.State
	to   domain.State
}

type runStoreStub struct {
	createRun        *domain.Run
	getRun           *domain.Run
	backendRef       string
	errorSummary     string
	stateUpdates     []runStateUpdate
	setBackendRefErr error
}

func (s *runStoreStub) Create(_ context.Context, run *domain.Run) error {
	s.createRun = run
	return nil
}

func (s *runStoreStub) Get(context.Context, string) (*domain.Run, error) {
	if s.getRun != nil {
		return s.getRun, nil
	}
	return nil, nil
}

func (s *runStoreStub) GetStatus(context.Context, string) (domain.State, error) {
	return domain.StateQueued, nil
}

func (s *runStoreStub) List(context.Context, repository.Page) (*repository.PageResult[*domain.Run], error) {
	return nil, nil
}

func (s *runStoreStub) UpdateState(_ context.Context, _ string, from, to domain.State) error {
	s.stateUpdates = append(s.stateUpdates, runStateUpdate{from: from, to: to})
	return nil
}

func (s *runStoreStub) SetError(_ context.Context, _ string, _ domain.State, summary string) error {
	s.errorSummary = summary
	return nil
}

func (s *runStoreStub) CountByState(context.Context) (map[domain.State]int, error) { return nil, nil }

func (s *runStoreStub) ListActiveRuns(context.Context, int) ([]*domain.Run, error) { return nil, nil }

func (s *runStoreStub) SetBackendRef(_ context.Context, _ string, ref string) error {
	s.backendRef = ref
	return s.setBackendRefErr
}

func (s *runStoreStub) SetBackendState(context.Context, string, json.RawMessage) error { return nil }
func (s *runStoreStub) SetWorkspaceBinding(context.Context, string, *domain.WorkspaceBinding) error {
	return nil
}

type taskStoreStub struct{}

func (s *taskStoreStub) Create(context.Context, *domain.Task) error               { return nil }
func (s *taskStoreStub) Get(context.Context, string) (*domain.Task, error)        { return nil, nil }
func (s *taskStoreStub) GetByTESID(context.Context, string) (*domain.Task, error) { return nil, nil }
func (s *taskStoreStub) List(context.Context, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return &repository.PageResult[*domain.Task]{}, nil
}
func (s *taskStoreStub) ListByRunID(context.Context, string, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return &repository.PageResult[*domain.Task]{}, nil
}
func (s *taskStoreStub) ListActiveTasks(context.Context, int) ([]*domain.Task, error) {
	return nil, nil
}
func (s *taskStoreStub) UpdateState(context.Context, string, domain.State, domain.State) error {
	return nil
}
func (s *taskStoreStub) SetExitCode(context.Context, string, int32) error    { return nil }
func (s *taskStoreStub) SetBackendRef(context.Context, string, string) error { return nil }
func (s *taskStoreStub) SetBackendAssignment(context.Context, string, string, string, json.RawMessage) error {
	return nil
}
func (s *taskStoreStub) SetBackendState(context.Context, string, json.RawMessage) error { return nil }
func (s *taskStoreStub) SetOutputLogs(context.Context, string, json.RawMessage) error   { return nil }

type eventStoreStub struct{}

func (s *eventStoreStub) Append(context.Context, *domain.Event) error { return nil }
func (s *eventStoreStub) ListByRunID(context.Context, string) ([]*domain.Event, error) {
	return nil, nil
}

type artifactStoreStub struct {
	objects map[string][]byte
	listErr error
	listed  int // objects passed to ListObjects callbacks
}

var _ objstore.Client = (*artifactStoreStub)(nil)

func (s *artifactStoreStub) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	data, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, errors.New("not found")
	}
	return ioutil.NopCloser(bytes.NewReader(data)), nil
}

func (s *artifactStoreStub) Put(context.Context, string, string, io.Reader, int64) error { return nil }
func (s *artifactStoreStub) Delete(context.Context, string, string) error                { return nil }
func (s *artifactStoreStub) Exists(context.Context, string, string) (bool, error)        { return false, nil }
func (s *artifactStoreStub) ListPrefix(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (s *artifactStoreStub) ListObjects(_ context.Context, bucket, prefix string, fn func(objstore.ObjectInfo) bool) error {
	if s.listErr != nil {
		return s.listErr
	}
	var keys []string
	for fullKey := range s.objects {
		key, ok := strings.CutPrefix(fullKey, bucket+"/")
		if ok && strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		s.listed++
		if !fn(objstore.ObjectInfo{Key: key, Size: int64(len(s.objects[bucket+"/"+key]))}) {
			return nil
		}
	}
	return nil
}
