package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/anuragxxd/conquest/internal/adapter"
	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
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
				Image:   "example.com/conquest-runner:nf-24.10.4",
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
	if runBackend.spec == nil || runBackend.spec.Image != "example.com/conquest-runner:nf-24.10.4" {
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
				Image:   "example.com/conquest-runner:nf-24.10.4",
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
	backendRef       string
	errorSummary     string
	stateUpdates     []runStateUpdate
	setBackendRefErr error
}

func (s *runStoreStub) Create(_ context.Context, run *domain.Run) error {
	s.createRun = run
	return nil
}

func (s *runStoreStub) Get(context.Context, string) (*domain.Run, error) { return nil, nil }

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
func (s *taskStoreStub) SetExitCode(context.Context, string, int32) error               { return nil }
func (s *taskStoreStub) SetBackendRef(context.Context, string, string) error            { return nil }
func (s *taskStoreStub) SetBackendState(context.Context, string, json.RawMessage) error { return nil }
func (s *taskStoreStub) SetOutputLogs(context.Context, string, json.RawMessage) error   { return nil }

type eventStoreStub struct{}

func (s *eventStoreStub) Append(context.Context, *domain.Event) error { return nil }
func (s *eventStoreStub) ListByRunID(context.Context, string) ([]*domain.Event, error) {
	return nil, nil
}
