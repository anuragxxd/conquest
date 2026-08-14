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

// orderRecorder records the sequence of cleanup operations so the test can
// assert that Jobs are deleted *before* the PVC/PV are released. Getting this
// backwards leaves the PVC pinned by pvc-protection for the whole Job TTL.
type orderRecorder struct {
	seq []string
}

type orderedBackend struct {
	runBackendRecorder
	rec *orderRecorder
}

func (o *orderedBackend) DeleteRunJobs(_ context.Context, runID string) error {
	o.rec.seq = append(o.rec.seq, "delete-jobs")
	return nil
}

type orderedCleaner struct {
	rec *orderRecorder
}

func (o *orderedCleaner) Cleanup(context.Context, *domain.WorkspaceBinding) error {
	o.rec.seq = append(o.rec.seq, "cleanup-workspace")
	return nil
}

func TestRunReconciler_DeletesJobsBeforeWorkspaceCleanup(t *testing.T) {
	for _, kind := range []domain.WorkspaceKind{domain.WorkspaceKindObjectFS, domain.WorkspaceKindPVC} {
		t.Run(string(kind), func(t *testing.T) {
			rec := &orderRecorder{}
			be := &orderedBackend{
				runBackendRecorder: runBackendRecorder{
					status: &backend.BackendStatus{Phase: domain.StateComplete, Reason: "done"},
				},
				rec: rec,
			}
			r := NewRunReconciler(
				&runStoreRecorder{}, &runEventStoreRecorder{}, be,
				DefaultConfig(),
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			r.SetWorkspaceCleaner(&orderedCleaner{rec: rec})

			run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")
			run.State = domain.StateRunning
			run.BackendRef = "runner-job"
			run.Workspace = &domain.WorkspaceBinding{
				Kind:      kind,
				ClaimName: "conquest-workspace-x",
				PVName:    "conquest-objectfs-x",
			}

			r.reconcileRun(context.Background(), run)

			want := []string{"delete-jobs", "cleanup-workspace"}
			if len(rec.seq) != len(want) {
				t.Fatalf("cleanup sequence = %v, want %v", rec.seq, want)
			}
			for i := range want {
				if rec.seq[i] != want[i] {
					t.Fatalf("cleanup sequence = %v, want %v", rec.seq, want)
				}
			}
		})
	}
}

// emptydir runs have no PVC to protect, so neither step should fire.
func TestRunReconciler_NoCleanupForEphemeralWorkspace(t *testing.T) {
	rec := &orderRecorder{}
	be := &orderedBackend{
		runBackendRecorder: runBackendRecorder{
			status: &backend.BackendStatus{Phase: domain.StateComplete, Reason: "done"},
		},
		rec: rec,
	}
	r := NewRunReconciler(
		&runStoreRecorder{}, &runEventStoreRecorder{}, be,
		DefaultConfig(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	r.SetWorkspaceCleaner(&orderedCleaner{rec: rec})

	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")
	run.State = domain.StateRunning
	run.BackendRef = "runner-job"
	run.Workspace = nil

	r.reconcileRun(context.Background(), run)

	if len(rec.seq) != 0 {
		t.Fatalf("expected no cleanup for emptydir, got %v", rec.seq)
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
	status         *backend.BackendStatus
	err            error
	deletedRunJobs []string
}

func (s *runBackendRecorder) SubmitRun(context.Context, *domain.Run, *backend.RunnerSpec) (*backend.RunSubmission, error) {
	return nil, nil
}

func (s *runBackendRecorder) CancelRun(context.Context, *domain.Run) error { return nil }

func (s *runBackendRecorder) RefreshRun(context.Context, *domain.Run) (*backend.BackendStatus, error) {
	return s.status, s.err
}

func (s *runBackendRecorder) DeleteRunJobs(_ context.Context, runID string) error {
	s.deletedRunJobs = append(s.deletedRunJobs, runID)
	return nil
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
