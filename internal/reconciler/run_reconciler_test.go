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

func TestRunReconciler_CancelingMapsTerminalStateToCanceled(t *testing.T) {
	runs := &runStoreRecorder{}
	events := &runEventStoreRecorder{}
	r := NewRunReconciler(
		runs,
		events,
		&runBackendRecorder{status: &backend.BackendStatus{Phase: domain.StateComplete, Reason: "runner completed", PodName: "pod-1"}},
		DefaultConfig(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")
	run.State = domain.StateCanceling
	run.BackendRef = "runner-job"

	r.reconcileRun(context.Background(), run)

	if len(runs.stateUpdates) != 1 {
		t.Fatalf("state updates = %d", len(runs.stateUpdates))
	}
	if got := runs.stateUpdates[0].to; got != domain.StateCanceled {
		t.Fatalf("target state = %s", got)
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %d", len(events.events))
	}
}

func TestRunReconciler_SkipsInvalidTransition(t *testing.T) {
	runs := &runStoreRecorder{}
	r := NewRunReconciler(
		runs,
		&runEventStoreRecorder{},
		&runBackendRecorder{status: &backend.BackendStatus{Phase: domain.StateQueued, Reason: "bad transition"}},
		DefaultConfig(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")
	run.State = domain.StateRunning
	run.BackendRef = "runner-job"

	r.reconcileRun(context.Background(), run)

	if len(runs.stateUpdates) != 0 {
		t.Fatalf("unexpected state updates: %+v", runs.stateUpdates)
	}
}

type runBackendRecorder struct {
	status *backend.BackendStatus
	err    error
}

func (s *runBackendRecorder) SubmitRun(context.Context, *domain.Run, *backend.RunnerSpec) (*backend.RunSubmission, error) {
	return nil, nil
}

func (s *runBackendRecorder) CancelRun(context.Context, *domain.Run) error { return nil }

func (s *runBackendRecorder) RefreshRun(context.Context, *domain.Run) (*backend.BackendStatus, error) {
	return s.status, s.err
}

type runStoreRecorder struct {
	stateUpdates []runStateUpdateRecord
}

type runStateUpdateRecord struct {
	from domain.State
	to   domain.State
}

func (s *runStoreRecorder) Create(context.Context, *domain.Run) error        { return nil }
func (s *runStoreRecorder) Get(context.Context, string) (*domain.Run, error) { return nil, nil }
func (s *runStoreRecorder) GetStatus(context.Context, string) (domain.State, error) {
	return domain.StateQueued, nil
}
func (s *runStoreRecorder) List(context.Context, repository.Page) (*repository.PageResult[*domain.Run], error) {
	return nil, nil
}
func (s *runStoreRecorder) UpdateState(_ context.Context, _ string, from, to domain.State) error {
	s.stateUpdates = append(s.stateUpdates, runStateUpdateRecord{from: from, to: to})
	return nil
}
func (s *runStoreRecorder) SetError(context.Context, string, domain.State, string) error { return nil }
func (s *runStoreRecorder) CountByState(context.Context) (map[domain.State]int, error) {
	return nil, nil
}
func (s *runStoreRecorder) ListActiveRuns(context.Context, int) ([]*domain.Run, error) {
	return nil, nil
}
func (s *runStoreRecorder) SetBackendRef(context.Context, string, string) error { return nil }
func (s *runStoreRecorder) SetBackendState(context.Context, string, json.RawMessage) error {
	return nil
}
func (s *runStoreRecorder) SetWorkspaceBinding(context.Context, string, *domain.WorkspaceBinding) error {
	return nil
}

type runEventStoreRecorder struct {
	events []*domain.Event
}

func (s *runEventStoreRecorder) Append(_ context.Context, event *domain.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *runEventStoreRecorder) ListByRunID(context.Context, string) ([]*domain.Event, error) {
	return nil, nil
}
