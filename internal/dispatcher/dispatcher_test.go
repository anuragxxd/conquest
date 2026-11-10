package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

// store is an in-memory task store and dispatch queue.
type store struct {
	repository.TaskStore // unused methods panic

	mu       sync.Mutex
	tasks    map[string]*domain.Task
	attempts map[string]int
	notAfter map[string]time.Time // dispatch_not_before
	leased   map[string]bool
	deferred []time.Duration
	clock    func() time.Time
}

func newStore(ids ...string) *store {
	s := &store{tasks: map[string]*domain.Task{}, attempts: map[string]int{}, notAfter: map[string]time.Time{},
		leased: map[string]bool{}, clock: time.Now}
	for _, id := range ids {
		s.tasks[id] = &domain.Task{TaskID: id, TESTaskID: id, RunID: "run-1", State: domain.StateQueued, CreatedAt: time.Now()}
	}
	return s
}

func (s *store) ClaimForDispatch(_ context.Context, limit int, _ time.Duration) ([]repository.DispatchClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []repository.DispatchClaim
	for id, t := range s.tasks {
		if len(out) == limit {
			break
		}
		if t.State != domain.StateQueued || t.BackendRef != "" || s.leased[id] || s.clock().Before(s.notAfter[id]) {
			continue
		}
		s.attempts[id]++
		s.leased[id] = true
		out = append(out, repository.DispatchClaim{TaskID: id, Attempt: s.attempts[id]})
	}
	return out, nil
}

func (s *store) DeferDispatch(_ context.Context, id string, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leased[id] = false
	s.notAfter[id] = notBefore
	s.deferred = append(s.deferred, notBefore.Sub(s.clock()).Round(time.Second))
	return nil
}

func (s *store) Get(_ context.Context, id string) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, errors.New("not found")
	}
	c := *t
	return &c, nil
}

func (s *store) UpdateState(_ context.Context, id string, from, to domain.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[id]
	if t.State != from {
		return errors.New("concurrent update")
	}
	t.State = to
	return nil
}

func (s *store) SetBackendAssignment(_ context.Context, id, name, ref string, route json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[id]
	t.BackendName, t.BackendRef, t.Route = name, ref, route
	return nil
}

func (s *store) state(id string) domain.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id].State
}

type events struct {
	mu   sync.Mutex
	list []*domain.Event
}

func (e *events) Append(_ context.Context, ev *domain.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
	return nil
}
func (e *events) ListByRunID(context.Context, string) ([]*domain.Event, error) { return nil, nil }

// fakeBackend answers Submit with the next scripted result.
type fakeBackend struct {
	mu        sync.Mutex
	results   []error
	submitted []string
	cancelled []string
	onSubmit  func(task *domain.Task) // runs before the result is returned
}

func (b *fakeBackend) Submit(_ context.Context, task *domain.Task) error {
	if b.onSubmit != nil {
		b.onSubmit(task)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.submitted = append(b.submitted, task.TaskID)
	var err error
	if len(b.results) > 0 {
		err, b.results = b.results[0], b.results[1:]
	}
	if err == nil {
		task.BackendRef = "job-" + task.TaskID
		task.Route = json.RawMessage(`{"chosen":"local"}`)
	}
	return err
}

func (b *fakeBackend) Cancel(_ context.Context, task *domain.Task) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelled = append(b.cancelled, task.TaskID)
	return nil
}

func (b *fakeBackend) Refresh(context.Context, *domain.Task) (*backend.BackendStatus, error) {
	return nil, nil
}

func newDispatcher(s *store, ev *events, be *fakeBackend, cfg Config) *Dispatcher {
	return New(s, s, ev, be, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// dispatchAll claims and dispatches every claimable task once, in order.
func dispatchAll(t *testing.T, d *Dispatcher) {
	t.Helper()
	claims, err := d.queue.ClaimForDispatch(context.Background(), 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		d.dispatch(context.Background(), c)
	}
}

func TestDispatchSubmitsAndMarksInitializing(t *testing.T) {
	s, ev, be := newStore("t1"), &events{}, &fakeBackend{}
	dispatchAll(t, newDispatcher(s, ev, be, Config{}))

	if got := s.state("t1"); got != domain.StateInitializing {
		t.Fatalf("state = %s, want INITIALIZING", got)
	}
	if ref := s.tasks["t1"].BackendRef; ref != "job-t1" {
		t.Fatalf("backend_ref = %q", ref)
	}
	if len(ev.list) != 1 || ev.list[0].Type != domain.EventTaskStateChanged {
		t.Fatalf("events = %+v", ev.list)
	}
}

func TestRetryableFailureIsRetriedWithBackoff(t *testing.T) {
	s, be := newStore("t1"), &fakeBackend{results: []error{backend.Retryable(errors.New("etcdserver: request timed out")), nil}}
	now := time.Now()
	s.clock = func() time.Time { return now }
	d := newDispatcher(s, &events{}, be, Config{MinBackoff: 2 * time.Second, MaxBackoff: time.Minute})
	d.now = s.clock

	dispatchAll(t, d)
	if got := s.state("t1"); got != domain.StateQueued {
		t.Fatalf("after a retryable failure: state = %s, want QUEUED", got)
	}
	if len(s.deferred) != 1 || s.deferred[0] != 2*time.Second {
		t.Fatalf("deferred = %v, want [2s]", s.deferred)
	}

	dispatchAll(t, d) // before the delay: nothing to claim
	if len(be.submitted) != 1 {
		t.Fatalf("submitted before the retry delay: %v", be.submitted)
	}

	now = now.Add(3 * time.Second)
	dispatchAll(t, d)
	if got := s.state("t1"); got != domain.StateInitializing {
		t.Fatalf("after the retry: state = %s, want INITIALIZING", got)
	}
	if s.attempts["t1"] != 2 {
		t.Fatalf("attempts = %d, want 2", s.attempts["t1"])
	}
}

func TestRetryableFailuresStopAfterMaxAttempts(t *testing.T) {
	retryable := backend.Retryable(errors.New("no backend accepted the task"))
	s, be := newStore("t1"), &fakeBackend{results: []error{retryable, retryable, retryable}}
	now := time.Now()
	s.clock = func() time.Time { return now }
	d := newDispatcher(s, &events{}, be, Config{MaxAttempts: 3})
	d.now = s.clock

	for i := 0; i < 3; i++ {
		dispatchAll(t, d)
		now = now.Add(time.Hour)
	}
	if got := s.state("t1"); got != domain.StateSystemError {
		t.Fatalf("state = %s, want SYSTEM_ERROR", got)
	}
	if len(be.submitted) != 3 {
		t.Fatalf("submissions = %d, want 3", len(be.submitted))
	}
}

func TestFailureThatMayHaveCreatedTheTaskIsNotRetried(t *testing.T) {
	s := newStore("t1")
	be := &fakeBackend{
		results: []error{errors.New("endpoint funnel-a outcome unknown: timeout")},
		onSubmit: func(task *domain.Task) {
			task.BackendName = "funnel-a"
			task.Route = json.RawMessage(`{"chosen":""}`)
		},
	}
	dispatchAll(t, newDispatcher(s, &events{}, be, Config{}))

	if got := s.state("t1"); got != domain.StateSystemError {
		t.Fatalf("state = %s, want SYSTEM_ERROR", got)
	}
	if len(s.deferred) != 0 {
		t.Fatalf("a submission that may have created the task was deferred for a retry")
	}
	if s.tasks["t1"].BackendName != "funnel-a" || s.tasks["t1"].Route == nil {
		t.Fatalf("route of the failed submission not kept: %+v", s.tasks["t1"])
	}
}

func TestCancelledTaskIsNotSubmitted(t *testing.T) {
	s, be := newStore("t1"), &fakeBackend{}
	d := newDispatcher(s, &events{}, be, Config{})
	claims, _ := s.ClaimForDispatch(context.Background(), 1, time.Minute)
	s.tasks["t1"].State = domain.StateCanceled // cancelled after the claim

	d.dispatch(context.Background(), claims[0])
	if len(be.submitted) != 0 {
		t.Fatalf("submitted a cancelled task")
	}
}

func TestTaskCancelledDuringSubmissionIsCancelledInTheBackend(t *testing.T) {
	s := newStore("t1")
	be := &fakeBackend{onSubmit: func(*domain.Task) {
		s.mu.Lock()
		s.tasks["t1"].State = domain.StateCanceled
		s.mu.Unlock()
	}}
	dispatchAll(t, newDispatcher(s, &events{}, be, Config{}))

	if got := s.state("t1"); got != domain.StateCanceled {
		t.Fatalf("state = %s, want CANCELED", got)
	}
	if len(be.cancelled) != 1 {
		t.Fatalf("submission of the cancelled task was not cancelled")
	}
}

func TestSlowSubmissionDoesNotHoldUpOtherTasks(t *testing.T) {
	s := newStore("slow", "fast")
	release := make(chan struct{})
	be := &fakeBackend{onSubmit: func(task *domain.Task) {
		if task.TaskID == "slow" {
			<-release
		}
	}}
	d := newDispatcher(s, &events{}, be, Config{Workers: 2, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for s.state("fast") != domain.StateInitializing {
		if time.Now().After(deadline) {
			t.Fatal("fast task not dispatched while the slow one was being submitted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.state("slow"); got != domain.StateQueued {
		t.Fatalf("slow task state = %s, want QUEUED while submitting", got)
	}
	close(release)
	for s.state("slow") != domain.StateInitializing {
		if time.Now().After(deadline) {
			t.Fatal("slow task not dispatched")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestBackoffDoublesUpToTheMaximum(t *testing.T) {
	d := newDispatcher(newStore(), &events{}, &fakeBackend{}, Config{MinBackoff: 2 * time.Second, MaxBackoff: time.Minute})
	var got []time.Duration
	for attempt := 1; attempt <= 7; attempt++ {
		got = append(got, d.backoff(attempt))
	}
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("backoff = %v, want %v seconds", got, want)
		}
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	d := newDispatcher(newStore(), &events{}, &fakeBackend{}, Config{})
	for i := 0; i < 100; i++ {
		d.Notify()
	}
}
