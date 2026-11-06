package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

// maxBody caps how much of a response is read.
const maxBody = 4 << 20

// systemLogTail is how many remote system log lines are kept with the task.
const systemLogTail = 10

// Backend forwards tasks to one remote TES endpoint.
type Backend struct {
	cfg       EndpointConfig
	client    *http.Client
	auth      authorizer
	gatewayID string
	storage   Storage
	log       *slog.Logger
	now       func() time.Time

	mu       sync.Mutex
	failures map[string]time.Time // task ID → first failed status lookup
}

var _ backend.TaskBackend = (*Backend)(nil)

// New creates a backend for an endpoint whose config has been validated.
// gatewayID identifies this Conquest in Via headers, for loop detection.
// storage may be nil unless the endpoint uses S3StyleGateway.
func New(cfg EndpointConfig, gatewayID string, storage Storage, log *slog.Logger) *Backend {
	client := &http.Client{
		Timeout: cfg.RequestTimeout.Duration,
		// A redirect could send the request, with its credentials, to
		// another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Backend{
		cfg:       cfg,
		client:    client,
		auth:      newAuthorizer(cfg.Auth, client),
		gatewayID: gatewayID,
		storage:   storage,
		log:       log.With("endpoint", cfg.Name),
		now:       time.Now,
		failures:  map[string]time.Time{},
	}
}

// Config returns the endpoint's configuration.
func (b *Backend) Config() *EndpointConfig { return &b.cfg }

// SubmitError is returned by Submit when the endpoint did not take the task.
type SubmitError struct {
	Endpoint string
	// Refused is true when the endpoint certainly did not create the task,
	// so it may be offered to another backend. When false, the endpoint
	// may have created it, and offering it elsewhere could run it twice.
	Refused bool
	Err     error
}

func (e *SubmitError) Error() string {
	outcome := "outcome unknown"
	if e.Refused {
		outcome = "refused"
	}
	return fmt.Sprintf("endpoint %s %s: %v", e.Endpoint, outcome, e.Err)
}

func (e *SubmitError) Unwrap() error { return e.Err }

// Submit creates the task on the endpoint and sets task.BackendRef to the
// endpoint's task ID. Errors are *SubmitError.
func (b *Backend) Submit(ctx context.Context, task *domain.Task) error {
	body, err := translate(ctx, task.TaskRequestPayload, &b.cfg, task.TaskID, b.storage)
	if err != nil {
		return &SubmitError{Endpoint: b.cfg.Name, Refused: true, Err: err}
	}
	resp, err := b.do(ctx, http.MethodPost, "/tasks", body)
	if err != nil {
		return &SubmitError{Endpoint: b.cfg.Name, Refused: refusedBeforeSending(err), Err: err}
	}
	if resp.status < 200 || resp.status > 299 {
		return &SubmitError{
			Endpoint: b.cfg.Name,
			// 4xx: the endpoint rejected the request. 503: it says it
			// can't take work now. Other 5xx (and 408/429 timeouts at a
			// proxy) leave it open whether the task was created.
			Refused: (resp.status >= 400 && resp.status < 500 && resp.status != http.StatusRequestTimeout) ||
				resp.status == http.StatusServiceUnavailable,
			Err: fmt.Errorf("HTTP %d: %s", resp.status, snippet(resp.body)),
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.body, &created); err != nil || created.ID == "" {
		return &SubmitError{Endpoint: b.cfg.Name, Err: fmt.Errorf("response has no task id: %s", snippet(resp.body))}
	}
	task.BackendRef = created.ID
	b.log.Info("submitted task to remote TES", "task_id", task.TaskID, "remote_task_id", created.ID)
	return nil
}

// Refresh reads the task's state on the endpoint.
func (b *Backend) Refresh(ctx context.Context, task *domain.Task) (*backend.BackendStatus, error) {
	if task.BackendRef == "" {
		return &backend.BackendStatus{Phase: task.State, Reason: "not submitted yet"}, nil
	}
	remote, status, err := b.getTask(ctx, task.BackendRef, "MINIMAL")
	if err == nil && remote != nil && remote.State.IsTerminal() {
		// terminal: one more read for exit code and logs
		remote, status, err = b.getTask(ctx, task.BackendRef, "FULL")
	}
	if err != nil {
		return b.lookupFailed(task, err)
	}
	b.clearFailure(task.TaskID)
	if status == http.StatusNotFound || remote == nil {
		return &backend.BackendStatus{
			Phase:   domain.StateSystemError,
			Reason:  "task not found on remote endpoint",
			Details: b.details(task, nil),
		}, nil
	}

	phase, reason := mapState(remote.State, task.State)
	st := &backend.BackendStatus{
		Phase:   phase,
		Reason:  reason,
		Details: b.details(task, remote),
	}
	if phase.IsTerminal() {
		st.ExitCode = remote.exitCode()
	}
	return st, nil
}

// Cancel asks the endpoint to cancel the task. Cancelling a task the
// endpoint no longer knows, or that has finished, is not an error.
func (b *Backend) Cancel(ctx context.Context, task *domain.Task) error {
	if task.BackendRef == "" {
		return nil
	}
	resp, err := b.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(task.BackendRef)+":cancel", []byte("{}"))
	if err != nil {
		return fmt.Errorf("cancel on endpoint %s: %w", b.cfg.Name, err)
	}
	// 404: gone. 400/409/412: already finished on some implementations.
	if resp.status >= 300 && resp.status != http.StatusNotFound && resp.status != http.StatusBadRequest &&
		resp.status != http.StatusConflict && resp.status != http.StatusPreconditionFailed {
		return fmt.Errorf("cancel on endpoint %s: HTTP %d: %s", b.cfg.Name, resp.status, snippet(resp.body))
	}
	return nil
}

// lookupFailed keeps the task's state while status lookups fail, and gives
// the task up once they have failed for longer than StaleAfter.
func (b *Backend) lookupFailed(task *domain.Task, err error) (*backend.BackendStatus, error) {
	b.mu.Lock()
	first, seen := b.failures[task.TaskID]
	if !seen {
		first = b.now()
		b.failures[task.TaskID] = first
	}
	b.mu.Unlock()
	if b.now().Sub(first) < b.cfg.StaleAfter.Duration {
		return nil, fmt.Errorf("status of remote task %s on endpoint %s: %w", task.BackendRef, b.cfg.Name, err)
	}
	b.clearFailure(task.TaskID)
	return &backend.BackendStatus{
		Phase:   domain.StateSystemError,
		Reason:  fmt.Sprintf("remote endpoint unreachable for %s: %v", b.cfg.StaleAfter.Duration, err),
		Details: b.details(task, nil),
	}, nil
}

func (b *Backend) clearFailure(taskID string) {
	b.mu.Lock()
	delete(b.failures, taskID)
	b.mu.Unlock()
}

func (b *Backend) details(task *domain.Task, remote *remoteTask) map[string]any {
	d := map[string]any{
		"endpoint":       b.cfg.Name,
		"remote_task_id": task.BackendRef,
	}
	if remote != nil {
		d["remote_state"] = string(remote.State)
		if logs := remote.systemLogs(); len(logs) > 0 {
			d["system_logs"] = logs
		}
	}
	return d
}

// mapState maps a remote TES state onto Conquest's task state machine,
// which skips QUEUED once submitted and has no PAUSED or CANCELING.
func mapState(remote, current domain.State) (domain.State, string) {
	switch remote {
	case domain.StateQueued, domain.StateInitializing:
		return domain.StateInitializing, "remote " + string(remote)
	case domain.StateRunning, domain.StatePaused:
		return domain.StateRunning, "remote " + string(remote)
	case domain.StateComplete, domain.StateExecutorError, domain.StateSystemError, domain.StateCanceled:
		return remote, "remote " + string(remote)
	case domain.StatePreempted:
		return domain.StateSystemError, "remote PREEMPTED"
	default: // UNKNOWN, CANCELING, or something new: keep the current state
		return current, "remote " + string(remote)
	}
}

type remoteTask struct {
	State domain.State `json:"state"`
	Logs  []struct {
		Logs []struct {
			ExitCode *int32 `json:"exit_code"`
		} `json:"logs"`
		SystemLogs []string `json:"system_logs"`
	} `json:"logs"`
}

// exitCode returns the exit code of the last executor of the last attempt
// that reports one.
func (t *remoteTask) exitCode() *int32 {
	if len(t.Logs) == 0 {
		return nil
	}
	last := t.Logs[len(t.Logs)-1].Logs
	for i := len(last) - 1; i >= 0; i-- {
		if last[i].ExitCode != nil {
			return last[i].ExitCode
		}
	}
	return nil
}

func (t *remoteTask) systemLogs() []string {
	if len(t.Logs) == 0 {
		return nil
	}
	logs := t.Logs[len(t.Logs)-1].SystemLogs
	if len(logs) > systemLogTail {
		logs = logs[len(logs)-systemLogTail:]
	}
	out := make([]string, len(logs))
	for i, l := range logs {
		if len(l) > 500 {
			l = l[:500] + "…"
		}
		out[i] = l
	}
	return out
}

func (b *Backend) getTask(ctx context.Context, id, view string) (*remoteTask, int, error) {
	resp, err := b.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id)+"?view="+view, nil)
	if err != nil {
		return nil, 0, err
	}
	if resp.status == http.StatusNotFound {
		return nil, resp.status, nil
	}
	if resp.status != http.StatusOK {
		return nil, resp.status, fmt.Errorf("HTTP %d: %s", resp.status, snippet(resp.body))
	}
	var t remoteTask
	if err := json.Unmarshal(resp.body, &t); err != nil {
		return nil, resp.status, fmt.Errorf("decode task: %w", err)
	}
	return &t, resp.status, nil
}

type response struct {
	status int
	body   []byte
}

func (b *Backend) do(ctx context.Context, method, path string, body []byte) (*response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.URL+path, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range b.cfg.Headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Via", "1.1 "+b.gatewayID)
	if err := b.auth.authorize(ctx, req); err != nil {
		return nil, &authError{err}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return &response{status: resp.StatusCode, body: data}, nil
}

type authError struct{ err error }

func (e *authError) Error() string { return "authenticate: " + e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

// refusedBeforeSending reports whether a request error means the request
// never reached the endpoint: no credentials, or no connection.
func refusedBeforeSending(err error) bool {
	var ae *authError
	if errors.As(err, &ae) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func snippet(b []byte) string {
	const limit = 300
	if len(b) > limit {
		return string(b[:limit]) + "…"
	}
	return string(b)
}
