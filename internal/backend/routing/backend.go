package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/backend/remote"
	"github.com/anuragxxd/conquest/internal/domain"
)

// RunLookup reads runs; repository.RunStore satisfies it.
type RunLookup interface {
	Get(ctx context.Context, runID string) (*domain.Run, error)
}

// Backend routes each task to the local backend or a remote endpoint, and
// sends later calls for the task to the backend that took it
// (task.BackendName).
type Backend struct {
	local   backend.TaskBackend
	remotes map[string]backend.TaskBackend
	router  *router
	runs    RunLookup
	log     *slog.Logger
	now     func() time.Time
}

var _ backend.TaskBackend = (*Backend)(nil)

// New creates a routing backend over the local backend and one remote
// backend per configured endpoint. bucket is Conquest's S3 bucket.
func New(cfg *Config, local backend.TaskBackend, runs RunLookup, bucket string, log *slog.Logger) *Backend {
	remotes := map[string]backend.TaskBackend{}
	for _, ep := range cfg.Endpoints {
		remotes[ep.Name] = remote.New(ep, cfg.GatewayID, log)
	}
	return newBackend(cfg, local, remotes, runs, bucket, log)
}

func newBackend(cfg *Config, local backend.TaskBackend, remotes map[string]backend.TaskBackend,
	runs RunLookup, bucket string, log *slog.Logger,
) *Backend {
	return &Backend{
		local:   local,
		remotes: remotes,
		router:  newRouter(cfg, bucket),
		runs:    runs,
		log:     log,
		now:     time.Now,
	}
}

// Submit routes the task and submits it. It sets task.BackendName,
// task.BackendRef and task.Route; the route is set on errors too.
//
// A backend that certainly refused the task is followed by the next
// candidate. If a remote endpoint may have created the task, Submit stops
// there: offering it elsewhere could run it twice.
func (b *Backend) Submit(ctx context.Context, task *domain.Task) error {
	run := b.runInfo(ctx, task.RunID)
	rec, err := b.router.decide(task, run, b.now())
	if rec != nil {
		defer func() { task.Route, _ = json.Marshal(rec) }()
	}
	if err != nil {
		return err
	}

	for _, name := range rec.Candidates {
		target := b.local
		if name != Local {
			target = b.remotes[name]
		}
		task.BackendName, task.BackendRef = "", ""
		err := target.Submit(ctx, task)
		if err == nil {
			rec.Attempts = append(rec.Attempts, Attempt{Backend: name, Outcome: "accepted"})
			rec.Chosen = name
			if name != Local {
				task.BackendName = name
			}
			b.log.Info("task routed",
				"task_id", task.TaskID, "backend", name, "rule", rec.Rule,
				"remote_task_id", task.BackendRef)
			return nil
		}
		var se *remote.SubmitError
		if name != Local && errors.As(err, &se) && se.Refused {
			rec.Attempts = append(rec.Attempts, Attempt{Backend: name, Outcome: "refused", Error: err.Error()})
			b.log.Warn("endpoint refused task; trying next backend", "task_id", task.TaskID, "endpoint", name, "err", err)
			continue
		}
		rec.Attempts = append(rec.Attempts, Attempt{Backend: name, Outcome: "unknown", Error: err.Error()})
		if name != Local {
			task.BackendName = name
		}
		return err
	}
	return fmt.Errorf("no backend accepted the task (tried %v)", rec.Candidates)
}

// Refresh asks the backend that took the task.
func (b *Backend) Refresh(ctx context.Context, task *domain.Task) (*backend.BackendStatus, error) {
	target, ok := b.backendFor(task)
	if !ok {
		return &backend.BackendStatus{
			Phase:  domain.StateSystemError,
			Reason: fmt.Sprintf("endpoint %q is no longer configured", task.BackendName),
		}, nil
	}
	return target.Refresh(ctx, task)
}

// Cancel asks the backend that took the task.
func (b *Backend) Cancel(ctx context.Context, task *domain.Task) error {
	target, ok := b.backendFor(task)
	if !ok {
		b.log.Warn("cannot cancel task on unconfigured endpoint", "task_id", task.TaskID, "endpoint", task.BackendName)
		return nil
	}
	return target.Cancel(ctx, task)
}

func (b *Backend) backendFor(task *domain.Task) (backend.TaskBackend, bool) {
	if task.BackendName == "" {
		return b.local, true
	}
	target, ok := b.remotes[task.BackendName]
	return target, ok
}

func (b *Backend) runInfo(ctx context.Context, runID string) runInfo {
	info := runInfo{Tags: map[string]string{}}
	if b.runs == nil || runID == "" {
		return info
	}
	run, err := b.runs.Get(ctx, runID)
	if err != nil {
		// Without the run, no rule on run tags or engine matches and the
		// data class is the default: the decision errs towards local.
		b.log.Warn("routing without run details", "run_id", runID, "err", err)
		return info
	}
	info.Engine = run.WorkflowEngine
	var req struct {
		Tags map[string]string `json:"tags"`
	}
	if json.Unmarshal(run.RequestPayload, &req) == nil && req.Tags != nil {
		info.Tags = req.Tags
	}
	return info
}
