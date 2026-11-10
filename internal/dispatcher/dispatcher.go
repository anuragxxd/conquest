// Package dispatcher routes and submits TES tasks in the background.
//
// The TES API stores a new task as QUEUED and answers at once; the
// dispatcher then claims it, submits it to its backend (Kubernetes or, with
// federation, a remote TES endpoint) and moves it to INITIALIZING, from where
// the reconciler follows it. A slow or briefly failing backend therefore
// delays tasks instead of failing the requests that create them: before,
// a client gave up while Conquest waited on a remote endpoint, or got a 500
// when the Kubernetes API timed out, and workflow engines end runs on
// either.
//
// Submissions that certainly created nothing (backend.IsRetryable) are
// retried with exponential backoff; other failures may have created the
// task, so the task ends SYSTEM_ERROR rather than risk running twice.
package dispatcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

// Config tunes the dispatcher.
type Config struct {
	// Workers is how many tasks are submitted at once, so that one slow
	// endpoint doesn't hold up tasks bound elsewhere.
	Workers int
	// Interval is how often the queue is checked without a Notify.
	Interval time.Duration
	// Lease is how long a claimed task belongs to this dispatcher; it must
	// exceed SubmitTimeout.
	Lease time.Duration
	// SubmitTimeout bounds one submission, routing included.
	SubmitTimeout time.Duration
	// MaxAttempts is how many submissions a task gets before it fails.
	MaxAttempts int
	// MinBackoff and MaxBackoff bound the delay between attempts, which
	// doubles from MinBackoff.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// DefaultConfig returns the defaults: 8 workers, 20 attempts over about
// 15 minutes.
func DefaultConfig() Config {
	return Config{
		Workers:       8,
		Interval:      time.Second,
		Lease:         5 * time.Minute,
		SubmitTimeout: 2 * time.Minute,
		MaxAttempts:   20,
		MinBackoff:    2 * time.Second,
		MaxBackoff:    time.Minute,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Workers <= 0 {
		c.Workers = d.Workers
	}
	if c.Interval <= 0 {
		c.Interval = d.Interval
	}
	if c.SubmitTimeout <= 0 {
		c.SubmitTimeout = d.SubmitTimeout
	}
	if c.Lease <= c.SubmitTimeout {
		c.Lease = c.SubmitTimeout + time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = d.MinBackoff
	}
	if c.MaxBackoff < c.MinBackoff {
		c.MaxBackoff = c.MinBackoff
	}
	return c
}

// Dispatcher submits undispatched tasks.
type Dispatcher struct {
	queue   repository.DispatchQueue
	tasks   repository.TaskStore
	events  repository.EventStore
	backend backend.TaskBackend
	cfg     Config
	log     *slog.Logger

	notify chan struct{}
	slots  chan struct{} // one per busy worker
	wg     sync.WaitGroup
	now    func() time.Time
}

// New creates a dispatcher.
func New(queue repository.DispatchQueue, tasks repository.TaskStore, events repository.EventStore,
	be backend.TaskBackend, cfg Config, log *slog.Logger) *Dispatcher {
	cfg = cfg.withDefaults()
	return &Dispatcher{
		queue:   queue,
		tasks:   tasks,
		events:  events,
		backend: be,
		cfg:     cfg,
		log:     log,
		notify:  make(chan struct{}, 1),
		slots:   make(chan struct{}, cfg.Workers),
		now:     time.Now,
	}
}

// Notify wakes the dispatcher, e.g. after a task was created. It never
// blocks.
func (d *Dispatcher) Notify() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

// Run dispatches until ctx is cancelled, then waits for submissions in
// flight.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("dispatcher started", "workers", d.cfg.Workers, "max_attempts", d.cfg.MaxAttempts)
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	for {
		d.drain(ctx)
		select {
		case <-ctx.Done():
			d.wg.Wait()
			d.log.Info("dispatcher stopped")
			return
		case <-ticker.C:
		case <-d.notify:
		}
	}
}

// drain claims and starts tasks while workers are free and tasks wait.
// Only as many tasks are claimed as there are free workers, so that claims
// don't sit on tasks that another replica could dispatch.
func (d *Dispatcher) drain(ctx context.Context) {
	for ctx.Err() == nil {
		free := cap(d.slots) - len(d.slots)
		if free == 0 {
			return
		}
		claims, err := d.queue.ClaimForDispatch(ctx, free, d.cfg.Lease)
		if err != nil {
			d.log.Error("failed to claim tasks for dispatch", "err", err)
			return
		}
		if len(claims) == 0 {
			return
		}
		for _, c := range claims {
			d.slots <- struct{}{}
			d.wg.Add(1)
			go func(c repository.DispatchClaim) {
				defer func() {
					<-d.slots
					d.wg.Done()
					d.Notify() // a worker is free: look for more work
				}()
				d.dispatch(ctx, c)
			}(c)
		}
	}
}

// dispatch submits one claimed task.
func (d *Dispatcher) dispatch(ctx context.Context, claim repository.DispatchClaim) {
	task, err := d.tasks.Get(ctx, claim.TaskID)
	if err != nil {
		// the lease expires and the task is claimed again
		d.log.Error("failed to load task for dispatch", "task_id", claim.TaskID, "err", err)
		return
	}
	if task.State != domain.StateQueued || task.BackendRef != "" {
		return // cancelled, or dispatched by an earlier claim
	}

	submitCtx, cancel := context.WithTimeout(ctx, d.cfg.SubmitTimeout)
	err = d.backend.Submit(submitCtx, task)
	cancel()
	if err != nil {
		d.failed(ctx, task, claim, err)
		return
	}

	if err := d.tasks.SetBackendAssignment(ctx, task.TaskID, task.BackendName, task.BackendRef, task.Route); err != nil {
		d.log.Error("task submitted, but its backend could not be recorded; cancelling it",
			"task_id", task.TaskID, "backend_ref", task.BackendRef, "err", err)
		_ = d.backend.Cancel(context.Background(), task)
		d.setState(ctx, task, domain.StateSystemError, "backend reference not recorded: "+err.Error())
		return
	}
	if !d.markInitializing(ctx, task) {
		return
	}
	d.appendStateEvent(ctx, task, domain.StateQueued, domain.StateInitializing, "dispatched")
	d.log.Info("task dispatched",
		"task_id", task.TaskID, "attempt", claim.Attempt,
		"backend", backendLabel(task), "backend_ref", task.BackendRef,
		"queued_for", d.now().Sub(task.CreatedAt).Round(time.Millisecond).String())
}

// markInitializing moves a submitted task from QUEUED to INITIALIZING, from
// where the reconciler follows it. If the task was cancelled meanwhile, the
// submission is cancelled too. It reports whether the task is INITIALIZING.
func (d *Dispatcher) markInitializing(ctx context.Context, task *domain.Task) bool {
	for attempt := 1; ; attempt++ {
		err := d.tasks.UpdateState(ctx, task.TaskID, domain.StateQueued, domain.StateInitializing)
		if err == nil {
			return true
		}
		current, getErr := d.tasks.Get(ctx, task.TaskID)
		if getErr == nil && current.State != domain.StateQueued {
			// cancelled while it was being submitted: undo the submission
			d.log.Info("task left QUEUED during dispatch; cancelling its submission",
				"task_id", task.TaskID, "state", current.State, "backend_ref", task.BackendRef)
			if cancelErr := d.backend.Cancel(context.Background(), task); cancelErr != nil {
				d.log.Warn("failed to cancel submission", "task_id", task.TaskID, "err", cancelErr)
			}
			return false
		}
		if attempt == 3 || ctx.Err() != nil {
			// still QUEUED with its backend recorded: the reconciler
			// moves it on (see reconciler.reconcileTask)
			d.log.Error("failed to mark submitted task INITIALIZING", "task_id", task.TaskID, "err", err)
			return false
		}
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
}

// failed handles a failed submission: retry it later if nothing was
// created and attempts remain, else fail the task.
func (d *Dispatcher) failed(ctx context.Context, task *domain.Task, claim repository.DispatchClaim, err error) {
	if task.Route != nil || task.BackendName != "" {
		// keep how the task was routed, for the audit trail
		if recErr := d.tasks.SetBackendAssignment(ctx, task.TaskID, task.BackendName, "", task.Route); recErr != nil {
			d.log.Warn("failed to record route of failed submission", "task_id", task.TaskID, "err", recErr)
		}
	}
	if backend.IsRetryable(err) && claim.Attempt < d.cfg.MaxAttempts {
		delay := d.backoff(claim.Attempt)
		if deferErr := d.queue.DeferDispatch(ctx, task.TaskID, d.now().Add(delay)); deferErr != nil {
			d.log.Error("failed to defer dispatch; the lease will expire", "task_id", task.TaskID, "err", deferErr)
		}
		d.log.Warn("task submission failed; will retry",
			"task_id", task.TaskID, "attempt", claim.Attempt, "retry_in", delay.String(), "err", err)
		return
	}
	reason := "submission failed: " + err.Error()
	if backend.IsRetryable(err) {
		reason = "submission failed after " + strconv.Itoa(claim.Attempt) + " attempts: " + err.Error()
	}
	d.log.Error("task submission failed", "task_id", task.TaskID, "attempt", claim.Attempt, "err", err)
	d.setState(ctx, task, domain.StateSystemError, reason)
}

// backoff is MinBackoff doubled per attempt after the first, at most
// MaxBackoff.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	delay := d.cfg.MinBackoff
	for i := 1; i < attempt && delay < d.cfg.MaxBackoff; i++ {
		delay *= 2
	}
	if delay > d.cfg.MaxBackoff {
		delay = d.cfg.MaxBackoff
	}
	return delay
}

func (d *Dispatcher) setState(ctx context.Context, task *domain.Task, to domain.State, reason string) {
	if err := d.tasks.UpdateState(ctx, task.TaskID, domain.StateQueued, to); err != nil {
		d.log.Warn("failed to update task state", "task_id", task.TaskID, "to", to, "err", err)
		return
	}
	d.appendStateEvent(ctx, task, domain.StateQueued, to, reason)
}

func (d *Dispatcher) appendStateEvent(ctx context.Context, task *domain.Task, from, to domain.State, reason string) {
	payload, _ := json.Marshal(domain.StateChangePayload{FromState: from, ToState: to, Reason: reason})
	if err := d.events.Append(ctx, domain.NewEvent(task.RunID, task.TaskID, domain.EventTaskStateChanged, payload)); err != nil {
		d.log.Warn("failed to append task state event", "task_id", task.TaskID, "err", err)
	}
}

func backendLabel(task *domain.Task) string {
	if task.BackendName == "" {
		return "local"
	}
	return task.BackendName
}
