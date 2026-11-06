package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anuragxxd/conquest/internal/domain"
)

// fakeTES is a minimal TES server that records requests.
type fakeTES struct {
	mu        sync.Mutex
	created   []map[string]any
	headers   []http.Header
	createRes func() (int, string)
	state     string
	exitCode  int
	sysLogs   []string
	cancelled []string
	getStatus int
}

func (f *fakeTES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Header.Clone())
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/ga4gh/tes/v1/tasks":
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		f.created = append(f.created, body)
		status, resp := http.StatusOK, `{"id":"remote-1"}`
		if f.createRes != nil {
			status, resp = f.createRes()
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":cancel"):
		f.cancelled = append(f.cancelled, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ga4gh/tes/v1/tasks/"), ":cancel"))
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ga4gh/tes/v1/tasks/"):
		if f.getStatus != 0 {
			w.WriteHeader(f.getStatus)
			return
		}
		task := map[string]any{"id": "remote-1", "state": f.state}
		if r.URL.Query().Get("view") == "FULL" {
			task["logs"] = []any{map[string]any{
				"logs":        []any{map[string]any{"exit_code": f.exitCode}},
				"system_logs": f.sysLogs,
			}}
		}
		_ = json.NewEncoder(w).Encode(task)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestBackend(t *testing.T, fake *fakeTES, mutate func(*EndpointConfig)) (*Backend, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := EndpointConfig{Name: "tes-a", URL: srv.URL + "/ga4gh/tes/v1/"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return New(cfg, "conquest-test", slog.New(slog.NewTextHandler(io.Discard, nil))), srv
}

const storedTask = `{
	"id": "local-id", "state": "QUEUED", "creation_time": "2026-01-01T00:00:00Z",
	"name": "sayHello (1)",
	"executors": [{"image": "busybox", "command": ["true"], "workdir": "/work"}],
	"inputs": [
		{"url": "s3://conquest/v1/workflow-runs/r1/work/.command.sh", "path": "/work/.command.sh"},
		{"url": "https://example.org/ref.fa", "path": "/work/ref.fa"},
		{"content": "x", "path": "/work/inline"}
	],
	"outputs": [{"url": "s3://conquest/v1/workflow-runs/r1/work/out%20file.txt", "path": "/work/out file.txt"}],
	"resources": {"cpu_cores": 2, "backend_parameters": {"k": "v"}},
	"tags": {"conquest_run_id": "r1", "conquest.tes_backend": "tes-a", "user": "kept"},
	"unmodelled_field": {"kept": true}
}`

func newTask() *domain.Task {
	return &domain.Task{TaskID: "t1", RunID: "r1", State: domain.StateQueued, TaskRequestPayload: json.RawMessage(storedTask)}
}

func TestSubmitTranslatesTheTask(t *testing.T) {
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, func(c *EndpointConfig) {
		c.S3URLStyle = S3StyleEndpoint
		c.S3Host = "s3.example.org:9000"
		c.Tags = map[string]string{"gateway.backends": `{"type":"allow"}`}
		c.Headers = map[string]string{"X-On-Behalf-Of": "alice"}
	})
	task := newTask()

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if task.BackendRef != "remote-1" {
		t.Fatalf("BackendRef = %q, want remote-1", task.BackendRef)
	}
	got := fake.created[0]
	for _, f := range []string{"id", "state", "creation_time"} {
		if _, ok := got[f]; ok {
			t.Errorf("server field %q was sent", f)
		}
	}
	if got["unmodelled_field"] == nil {
		t.Error("fields Conquest doesn't model must be kept")
	}
	wantTags := map[string]any{"user": "kept", "gateway.backends": `{"type":"allow"}`, TaskIDTag: "t1"}
	if tags, _ := json.Marshal(got["tags"]); string(tags) != mustJSON(wantTags) {
		t.Errorf("tags = %s, want %s", tags, mustJSON(wantTags))
	}
	inputs := got["inputs"].([]any)
	if u := inputs[0].(map[string]any)["url"]; u != "s3://s3.example.org:9000/conquest/v1/workflow-runs/r1/work/.command.sh" {
		t.Errorf("s3 input url = %v", u)
	}
	if u := inputs[1].(map[string]any)["url"]; u != "https://example.org/ref.fa" {
		t.Errorf("https input url changed: %v", u)
	}
	if u := got["outputs"].([]any)[0].(map[string]any)["url"]; u != "s3://s3.example.org:9000/conquest/v1/workflow-runs/r1/work/out%20file.txt" {
		t.Errorf("output url = %v", u)
	}
	h := fake.headers[0]
	if h.Get("Via") != "1.1 conquest-test" || h.Get("X-On-Behalf-Of") != "alice" {
		t.Errorf("headers = %v", h)
	}
}

func TestSubmitBucketStyleKeepsS3URLs(t *testing.T) {
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, nil)

	if err := b.Submit(context.Background(), newTask()); err != nil {
		t.Fatalf("submit: %v", err)
	}

	in := fake.created[0]["inputs"].([]any)[0].(map[string]any)["url"]
	if in != "s3://conquest/v1/workflow-runs/r1/work/.command.sh" {
		t.Errorf("url = %v", in)
	}
}

func TestSubmitClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		status  int
		refused bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusRequestTimeout, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusGatewayTimeout, false},
	} {
		fake := &fakeTES{createRes: func() (int, string) { return tc.status, `{"msg":"no"}` }}
		b, _ := newTestBackend(t, fake, nil)

		err := b.Submit(context.Background(), newTask())

		var se *SubmitError
		if !errors.As(err, &se) || se.Refused != tc.refused {
			t.Errorf("HTTP %d: err = %v, want refused=%v", tc.status, err, tc.refused)
		}
	}
}

func TestSubmitWithoutTaskIDIsUnknownOutcome(t *testing.T) {
	fake := &fakeTES{createRes: func() (int, string) { return http.StatusOK, `{}` }}
	b, _ := newTestBackend(t, fake, nil)

	err := b.Submit(context.Background(), newTask())

	var se *SubmitError
	if !errors.As(err, &se) || se.Refused {
		t.Fatalf("err = %v, want unknown outcome", err)
	}
}

func TestSubmitToUnreachableEndpointIsRefused(t *testing.T) {
	b, srv := newTestBackend(t, &fakeTES{}, nil)
	srv.Close()

	err := b.Submit(context.Background(), newTask())

	var se *SubmitError
	if !errors.As(err, &se) || !se.Refused {
		t.Fatalf("err = %v, want refused", err)
	}
}

func TestSubmitWithoutCredentialsIsRefusedAndSendsNothing(t *testing.T) {
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, func(c *EndpointConfig) {
		c.Auth = AuthConfig{Type: AuthBearer, TokenEnv: "CONQUEST_TEST_UNSET_TOKEN"}
	})

	err := b.Submit(context.Background(), newTask())

	var se *SubmitError
	if !errors.As(err, &se) || !se.Refused || len(fake.created) != 0 {
		t.Fatalf("err = %v, requests = %d; want refused before sending", err, len(fake.created))
	}
}

func TestRefreshMapsStates(t *testing.T) {
	for remoteState, want := range map[string]domain.State{
		"QUEUED":         domain.StateInitializing,
		"INITIALIZING":   domain.StateInitializing,
		"RUNNING":        domain.StateRunning,
		"PAUSED":         domain.StateRunning,
		"EXECUTOR_ERROR": domain.StateExecutorError,
		"SYSTEM_ERROR":   domain.StateSystemError,
		"CANCELED":       domain.StateCanceled,
		"PREEMPTED":      domain.StateSystemError,
		"CANCELING":      domain.StateRunning, // kept
		"UNKNOWN":        domain.StateRunning, // kept
	} {
		b, _ := newTestBackend(t, &fakeTES{state: remoteState}, nil)
		task := newTask()
		task.State, task.BackendRef = domain.StateRunning, "remote-1"

		st, err := b.Refresh(context.Background(), task)

		if err != nil || st.Phase != want {
			t.Errorf("remote %s: phase = %v, err = %v; want %s", remoteState, st, err, want)
		}
	}
}

func TestRefreshCompleteReadsExitCodeAndLogs(t *testing.T) {
	b, _ := newTestBackend(t, &fakeTES{state: "COMPLETE", exitCode: 0, sysLogs: []string{"upload finished"}}, nil)
	task := newTask()
	task.State, task.BackendRef = domain.StateRunning, "remote-1"

	st, err := b.Refresh(context.Background(), task)

	if err != nil || st.Phase != domain.StateComplete || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
	if st.Details["endpoint"] != "tes-a" || st.Details["remote_task_id"] != "remote-1" {
		t.Errorf("details = %v", st.Details)
	}
	if logs, _ := st.Details["system_logs"].([]string); len(logs) != 1 {
		t.Errorf("system logs = %v", st.Details["system_logs"])
	}
}

func TestRefreshOfMissingTaskIsSystemError(t *testing.T) {
	b, _ := newTestBackend(t, &fakeTES{getStatus: http.StatusNotFound}, nil)
	task := newTask()
	task.State, task.BackendRef = domain.StateRunning, "remote-1"

	st, err := b.Refresh(context.Background(), task)

	if err != nil || st.Phase != domain.StateSystemError {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
}

func TestRefreshGivesUpAfterStaleAfter(t *testing.T) {
	b, srv := newTestBackend(t, &fakeTES{}, func(c *EndpointConfig) { c.StaleAfter.Duration = time.Minute })
	srv.Close()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	task := newTask()
	task.State, task.BackendRef = domain.StateRunning, "remote-1"

	if _, err := b.Refresh(context.Background(), task); err == nil {
		t.Fatal("first failure: want an error, so the state is kept")
	}
	clock = clock.Add(59 * time.Second)
	if _, err := b.Refresh(context.Background(), task); err == nil {
		t.Fatal("before StaleAfter: want an error")
	}
	clock = clock.Add(2 * time.Second)
	st, err := b.Refresh(context.Background(), task)
	if err != nil || st.Phase != domain.StateSystemError {
		t.Fatalf("after StaleAfter: status = %+v, err = %v", st, err)
	}
}

func TestCancel(t *testing.T) {
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, nil)
	task := newTask()
	task.BackendRef = "remote-1"

	if err := b.Cancel(context.Background(), task); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0] != "remote-1" {
		t.Errorf("cancelled = %v", fake.cancelled)
	}
	task.BackendRef = ""
	if err := b.Cancel(context.Background(), task); err != nil || len(fake.cancelled) != 1 {
		t.Errorf("cancel without remote id: err = %v, calls = %d", err, len(fake.cancelled))
	}
}

func TestClientCredentialsTokenIsCached(t *testing.T) {
	var tokenCalls int
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		user, pass, _ := r.BasicAuth()
		if r.FormValue("grant_type") != "client_credentials" || user != "conquest" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"tok","expires_in":3600}`)
	}))
	t.Cleanup(idp.Close)
	t.Setenv("CONQUEST_TEST_CLIENT_SECRET", "s3cret")
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, func(c *EndpointConfig) {
		c.Auth = AuthConfig{Type: AuthClientCredentials, TokenURL: idp.URL, ClientID: "conquest", ClientSecretEnv: "CONQUEST_TEST_CLIENT_SECRET"}
	})

	for range 3 {
		if err := b.Submit(context.Background(), newTask()); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	if tokenCalls != 1 {
		t.Errorf("token requests = %d, want 1", tokenCalls)
	}
	if got := fake.headers[2].Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg EndpointConfig
		ok  bool
	}{
		"minimal":                {EndpointConfig{Name: "a", URL: "https://tes.example.org/v1"}, true},
		"reserved name":          {EndpointConfig{Name: "local", URL: "https://x"}, false},
		"bad name":               {EndpointConfig{Name: "A_b", URL: "https://x"}, false},
		"relative url":           {EndpointConfig{Name: "a", URL: "/v1"}, false},
		"credentials in url":     {EndpointConfig{Name: "a", URL: "https://u:p@x/v1"}, false},
		"endpoint style no host": {EndpointConfig{Name: "a", URL: "https://x", S3URLStyle: S3StyleEndpoint}, false},
		"unknown style":          {EndpointConfig{Name: "a", URL: "https://x", S3URLStyle: "path"}, false},
		"basic without password": {EndpointConfig{Name: "a", URL: "https://x", Auth: AuthConfig{Type: AuthBasic, Username: "u"}}, false},
		"authorization header":   {EndpointConfig{Name: "a", URL: "https://x", Headers: map[string]string{"Authorization": "x"}}, false},
	} {
		err := tc.cfg.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestTES10DowngradesCompatibleTasksAndRefusesOthers(t *testing.T) {
	fake := &fakeTES{}
	b, _ := newTestBackend(t, fake, func(c *EndpointConfig) { c.TESVersion = TES10 })
	task := newTask()
	task.TaskRequestPayload = json.RawMessage(strings.Replace(storedTask, `"backend_parameters": {"k": "v"}`, `"backend_parameters_strict": false`, 1))

	if err := b.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res := fake.created[0]["resources"].(map[string]any); res["backend_parameters_strict"] != nil {
		t.Errorf("1.1 field sent to a 1.0 endpoint: %v", res)
	}

	glob := newTask()
	glob.TaskRequestPayload = json.RawMessage(strings.Replace(storedTask, `"path": "/work/out file.txt"`, `"path": "/work/*.txt", "path_prefix": "/work"`, 1))
	err := b.Submit(context.Background(), glob)
	var se *SubmitError
	if !errors.As(err, &se) || !se.Refused || !strings.Contains(err.Error(), "wildcard outputs") || len(fake.created) != 1 {
		t.Fatalf("err = %v, requests = %d; want refused before sending", err, len(fake.created))
	}
}
