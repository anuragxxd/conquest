package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

func TestReconcileTask_PersistsExitCodeBeforeTerminalState(t *testing.T) {
	store := &stubTaskStore{}
	events := &stubEventStore{}
	exitCode := int32(0)
	be := &stubBackend{
		status: &backend.BackendStatus{
			Phase:    domain.StateComplete,
			ExitCode: &exitCode,
			Reason:   "executor completed successfully",
			PodName:  "pod-1",
		},
	}
	r := New(store, events, be, DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	task := domain.NewTask("run-1")
	task.TESTaskID = task.TaskID
	task.State = domain.StateRunning

	r.reconcileTask(context.Background(), task)

	if len(store.calls) < 2 {
		t.Fatalf("calls = %v, want at least SetExitCode and UpdateState", store.calls)
	}
	if store.calls[0] != "SetExitCode" {
		t.Fatalf("first call = %q, want SetExitCode; calls=%v", store.calls[0], store.calls)
	}
	if store.calls[1] != "UpdateState" {
		t.Fatalf("second call = %q, want UpdateState; calls=%v", store.calls[1], store.calls)
	}
	if len(events.events) != 1 {
		t.Fatalf("events appended = %d, want 1", len(events.events))
	}
}

type stubBackend struct {
	status *backend.BackendStatus
	err    error
}

func (s *stubBackend) Submit(context.Context, *domain.Task) error { return nil }

func (s *stubBackend) Cancel(context.Context, *domain.Task) error { return nil }

func (s *stubBackend) Refresh(context.Context, *domain.Task) (*backend.BackendStatus, error) {
	return s.status, s.err
}

type stubTaskStore struct {
	calls []string
}

func (s *stubTaskStore) Create(context.Context, *domain.Task) error { return nil }

func (s *stubTaskStore) Get(context.Context, string) (*domain.Task, error) { return nil, nil }

func (s *stubTaskStore) GetByTESID(context.Context, string) (*domain.Task, error) { return nil, nil }

func (s *stubTaskStore) List(context.Context, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return nil, nil
}

func (s *stubTaskStore) ListByRunID(context.Context, string, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return nil, nil
}

func (s *stubTaskStore) ListActiveTasks(context.Context, int) ([]*domain.Task, error) {
	return nil, nil
}

func (s *stubTaskStore) UpdateState(context.Context, string, domain.State, domain.State) error {
	s.calls = append(s.calls, "UpdateState")
	return nil
}

func (s *stubTaskStore) SetExitCode(context.Context, string, int32) error {
	s.calls = append(s.calls, "SetExitCode")
	return nil
}

func (s *stubTaskStore) SetBackendRef(context.Context, string, string) error { return nil }

func (s *stubTaskStore) SetBackendState(context.Context, string, json.RawMessage) error {
	s.calls = append(s.calls, "SetBackendState")
	return nil
}

func (s *stubTaskStore) SetOutputLogs(context.Context, string, json.RawMessage) error { return nil }

type stubEventStore struct {
	events []*domain.Event
}

func (s *stubEventStore) Append(_ context.Context, event *domain.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *stubEventStore) ListByRunID(context.Context, string) ([]*domain.Event, error) {
	return nil, nil
}
