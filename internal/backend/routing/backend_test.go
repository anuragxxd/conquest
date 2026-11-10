package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/backend/remote"
	"github.com/anuragxxd/conquest/internal/domain"
)

type fakeBackend struct {
	name      string
	submitErr error
	submitted []string
	refreshed []string
	cancelled []string
}

func (f *fakeBackend) Submit(_ context.Context, task *domain.Task) error {
	f.submitted = append(f.submitted, task.TaskID)
	if f.submitErr != nil {
		return f.submitErr
	}
	task.BackendRef = f.name + "-ref"
	return nil
}

func (f *fakeBackend) Refresh(_ context.Context, task *domain.Task) (*backend.BackendStatus, error) {
	f.refreshed = append(f.refreshed, task.TaskID)
	return &backend.BackendStatus{Phase: domain.StateRunning, Reason: f.name}, nil
}

func (f *fakeBackend) Cancel(_ context.Context, task *domain.Task) error {
	f.cancelled = append(f.cancelled, task.TaskID)
	return nil
}

type fakeRuns map[string]*domain.Run

func (f fakeRuns) Get(_ context.Context, id string) (*domain.Run, error) {
	if r, ok := f[id]; ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

func setup(t *testing.T, runTags map[string]string) (*Backend, *fakeBackend, *fakeBackend, *fakeBackend) {
	t.Helper()
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{{Name: "qc", Match: Match{Engines: []string{"nextflow"}}, Backends: []string{"tesk-b", "funnel-a"}}}
	})
	local, funnel, tesk := &fakeBackend{name: "local"}, &fakeBackend{name: "funnel-a"}, &fakeBackend{name: "tesk-b"}
	payload, _ := json.Marshal(map[string]any{"tags": runTags})
	runs := fakeRuns{"r1": {RunID: "r1", WorkflowEngine: "nextflow", RequestPayload: payload}}
	b := newBackend(cfg, local, map[string]backend.TaskBackend{"funnel-a": funnel, "tesk-b": tesk}, runs, "conquest",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return b, local, funnel, tesk
}

func record(t *testing.T, task *domain.Task) Record {
	t.Helper()
	var rec Record
	if err := json.Unmarshal(task.Route, &rec); err != nil {
		t.Fatalf("route: %v (%s)", err, task.Route)
	}
	return rec
}

func TestSubmitGoesToFirstCandidate(t *testing.T) {
	b, local, _, tesk := setup(t, map[string]string{DataClassTag: "public"})
	task := taskWith(nfTask)

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if task.BackendName != "tesk-b" || task.BackendRef != "tesk-b-ref" || len(tesk.submitted) != 1 || len(local.submitted) != 0 {
		t.Fatalf("task = %+v", task)
	}
	rec := record(t, task)
	if rec.Chosen != "tesk-b" || rec.Rule != "qc" || len(rec.Attempts) != 1 || rec.Attempts[0].Outcome != "accepted" {
		t.Errorf("record = %+v", rec)
	}
}

func TestSubmitFallsBackAfterCertainRefusal(t *testing.T) {
	b, local, funnel, tesk := setup(t, map[string]string{DataClassTag: "public"})
	tesk.submitErr = &remote.SubmitError{Endpoint: "tesk-b", Refused: true, Err: errors.New("HTTP 503")}
	funnel.submitErr = &remote.SubmitError{Endpoint: "funnel-a", Refused: true, Err: errors.New("connection refused")}
	task := taskWith(nfTask)

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if task.BackendName != "" || task.BackendRef != "local-ref" || len(local.submitted) != 1 {
		t.Fatalf("task = %+v", task)
	}
	rec := record(t, task)
	var outcomes []string
	for _, a := range rec.Attempts {
		outcomes = append(outcomes, a.Backend+":"+a.Outcome)
	}
	if !slices.Equal(outcomes, []string{"tesk-b:refused", "funnel-a:refused", "local:accepted"}) || rec.Chosen != Local {
		t.Errorf("attempts = %v, chosen = %q", outcomes, rec.Chosen)
	}
}

func TestSubmitStopsWhenOutcomeIsUnknown(t *testing.T) {
	b, local, funnel, tesk := setup(t, map[string]string{DataClassTag: "public"})
	tesk.submitErr = &remote.SubmitError{Endpoint: "tesk-b", Refused: false, Err: errors.New("HTTP 502")}
	task := taskWith(nfTask)

	err := b.Submit(context.Background(), task)

	if err == nil || len(funnel.submitted) != 0 || len(local.submitted) != 0 {
		t.Fatalf("err = %v; a task that may exist on tesk-b was offered elsewhere", err)
	}
	if task.BackendName != "tesk-b" {
		t.Errorf("BackendName = %q, want tesk-b for the audit trail", task.BackendName)
	}
	if rec := record(t, task); rec.Attempts[0].Outcome != "unknown" || rec.Chosen != "" {
		t.Errorf("record = %+v", rec)
	}
}

func TestRunWithDisallowedDataClassStaysLocal(t *testing.T) {
	b, local, funnel, tesk := setup(t, map[string]string{DataClassTag: "patient"})
	task := taskWith(nfTask)

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(local.submitted) != 1 || len(funnel.submitted)+len(tesk.submitted) != 0 {
		t.Fatalf("local=%v funnel=%v tesk=%v", local.submitted, funnel.submitted, tesk.submitted)
	}
	if rec := record(t, task); len(rec.Excluded) != 2 {
		t.Errorf("exclusions = %v", rec.Excluded)
	}
}

func TestUnknownRunRoutesWithoutRunDetails(t *testing.T) {
	b, local, _, _ := setup(t, nil)
	task := taskWith(nfTask)
	task.RunID = "missing"

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// no engine, so the nextflow rule doesn't match
	if len(local.submitted) != 1 {
		t.Fatalf("local submissions = %d", len(local.submitted))
	}
}

func TestRefreshAndCancelGoToTheTasksBackend(t *testing.T) {
	b, local, funnel, _ := setup(t, nil)
	remoteTask := &domain.Task{TaskID: "t1", BackendName: "funnel-a", BackendRef: "x"}
	localTask := &domain.Task{TaskID: "t2", BackendRef: "job"}

	_, _ = b.Refresh(context.Background(), remoteTask)
	_ = b.Cancel(context.Background(), remoteTask)
	_, _ = b.Refresh(context.Background(), localTask)
	_ = b.Cancel(context.Background(), localTask)

	if !slices.Equal(funnel.refreshed, []string{"t1"}) || !slices.Equal(funnel.cancelled, []string{"t1"}) ||
		!slices.Equal(local.refreshed, []string{"t2"}) || !slices.Equal(local.cancelled, []string{"t2"}) {
		t.Errorf("funnel=%v/%v local=%v/%v", funnel.refreshed, funnel.cancelled, local.refreshed, local.cancelled)
	}
}

func TestTaskOnRemovedEndpointIsSystemError(t *testing.T) {
	b, _, _, _ := setup(t, nil)
	task := &domain.Task{TaskID: "t1", BackendName: "gone", BackendRef: "x"}

	st, err := b.Refresh(context.Background(), task)

	if err != nil || st.Phase != domain.StateSystemError {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
	if err := b.Cancel(context.Background(), task); err != nil {
		t.Errorf("cancel: %v", err)
	}
}

func TestAllCandidatesRefusingIsRetryable(t *testing.T) {
	b, local, funnel, tesk := setup(t, map[string]string{DataClassTag: "public"})
	refused := func(name string) error {
		return &remote.SubmitError{Endpoint: name, Refused: true, Err: errors.New("HTTP 503")}
	}
	tesk.submitErr, funnel.submitErr = refused("tesk-b"), refused("funnel-a")
	local.submitErr = backend.Retryable(errors.New("etcdserver: request timed out"))
	task := taskWith(nfTask)

	err := b.Submit(context.Background(), task)

	if !backend.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable: nothing was created", err)
	}
	if rec := record(t, task); rec.Attempts[len(rec.Attempts)-1].Outcome != "failed" {
		t.Errorf("record = %+v", rec)
	}
}

func TestUnknownOutcomeIsNotRetryable(t *testing.T) {
	b, _, _, tesk := setup(t, map[string]string{DataClassTag: "public"})
	tesk.submitErr = &remote.SubmitError{Endpoint: "tesk-b", Refused: false, Err: errors.New("HTTP 502")}

	if err := b.Submit(context.Background(), taskWith(nfTask)); backend.IsRetryable(err) {
		t.Fatalf("err = %v: a task that may exist on tesk-b must not be submitted again", err)
	}
}
