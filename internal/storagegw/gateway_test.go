package storagegw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anuragxxd/conquest/internal/domain"
)

type tasks map[string]*domain.Task

func (t tasks) Get(_ context.Context, id string) (*domain.Task, error) {
	if task, ok := t[id]; ok {
		return task, nil
	}
	return nil, errors.New("not found")
}

type presigner struct{ calls []string }

func (p *presigner) Presign(_ context.Context, method, bucket, key string, _ time.Duration) (*url.URL, error) {
	p.calls = append(p.calls, method+" "+bucket+"/"+key)
	return &url.URL{Scheme: "http", Host: "s3.example.org", Path: "/" + bucket + "/" + key,
		RawQuery: "X-Amz-Signature=sig&method=" + method}, nil
}

const request = `{
	"inputs": [
		{"url": "s3://conquest/run/work/ab/.command.sh", "path": "/work/.command.sh"},
		{"url": "s3://conquest/run/bundle", "path": "/work/workflow", "type": "DIRECTORY"},
		{"content": "x", "path": "/work/x"}
	],
	"outputs": [
		{"url": "s3://conquest/run/work/ab/.exitcode", "path": "/work/.exitcode"},
		{"url": "s3://conquest/run/work/ab", "path": "/work/*.html", "path_prefix": "/work"},
		{"url": "s3://conquest/run/results/dir", "path": "/work/dir", "type": "DIRECTORY"}
	]
}`

func newGateway(t *testing.T, task *domain.Task) (*Gateway, *presigner, http.Handler) {
	t.Helper()
	p := &presigner{}
	gw, err := New(Config{
		PublicURL: "http://conquest.example.org:30794/",
		Secret:    []byte(strings.Repeat("s", 32)),
		Buckets:   []string{"conquest"},
	}, tasks{"t1": task}, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	gw.Routes(r)
	return gw, p, r
}

func activeTask() *domain.Task {
	return &domain.Task{TaskID: "t1", State: domain.StateRunning, TaskRequestPayload: json.RawMessage(request)}
}

func do(t *testing.T, h http.Handler, method, rawURL string) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, u.RequestURI(), nil))
	return rec
}

func TestURLRoundTrip(t *testing.T) {
	gw, p, h := newGateway(t, activeTask())

	u := gw.URL("t1", "conquest", "run/work/ab/.command.sh")
	if !strings.HasPrefix(u, "http://conquest.example.org:30794/storage/v1/t/t1/") {
		t.Fatalf("url = %s", u)
	}
	rec := do(t, h, http.MethodGet, u)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "conquest/run/work/ab/.command.sh?") {
		t.Errorf("Location = %s", loc)
	}
	if len(p.calls) != 1 || p.calls[0] != "GET conquest/run/work/ab/.command.sh" {
		t.Errorf("presigned = %v", p.calls)
	}
}

func TestScope(t *testing.T) {
	gw, _, h := newGateway(t, activeTask())
	for _, tc := range []struct {
		method, key string
		want        int
	}{
		{http.MethodGet, "run/work/ab/.command.sh", http.StatusTemporaryRedirect},  // FILE input
		{http.MethodHead, "run/work/ab/.command.sh", http.StatusTemporaryRedirect}, // HEAD of an input
		{http.MethodGet, "run/bundle/Snakefile", http.StatusTemporaryRedirect},     // below a DIRECTORY input
		{http.MethodGet, "run/bundle-other/x", http.StatusForbidden},               // sibling prefix
		{http.MethodGet, "run/work/ab/.exitcode", http.StatusForbidden},            // an output, not an input
		{http.MethodPut, "run/work/ab/.command.sh", http.StatusTemporaryRedirect},  // in a FILE output's directory
		{http.MethodPut, "run/work/ab/.exitcode", http.StatusTemporaryRedirect},    // FILE output
		{http.MethodPut, "run/work/ab/report.html", http.StatusTemporaryRedirect},  // below a wildcard output
		{http.MethodPut, "run/results/dir/a/b.txt", http.StatusTemporaryRedirect},  // below a DIRECTORY output
		{http.MethodPut, "run/results/other.txt", http.StatusForbidden},            // next to a DIRECTORY output
		{http.MethodPut, "run/bundle/Snakefile", http.StatusForbidden},             // an input
		{http.MethodPut, "other-run/work/ab/.exitcode", http.StatusForbidden},      // another run
	} {
		rec := do(t, h, tc.method, gw.URL("t1", "conquest", tc.key))
		if rec.Code != tc.want {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.key, rec.Code, tc.want)
		}
	}
}

func TestRefusals(t *testing.T) {
	gw, p, h := newGateway(t, activeTask())
	good := gw.URL("t1", "conquest", "run/work/ab/.command.sh")

	forged := strings.Replace(good, "/t/t1/", "/t/t1/x", 1)
	if rec := do(t, h, http.MethodGet, forged); rec.Code != http.StatusNotFound {
		t.Errorf("forged signature: status = %d", rec.Code)
	}
	otherTask := strings.Replace(good, "/t/t1/", "/t/t2/", 1)
	if rec := do(t, h, http.MethodGet, otherTask); rec.Code != http.StatusNotFound {
		t.Errorf("signature of another task: status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, gw.URL("t1", "elsewhere", "run/work/ab/.command.sh")); rec.Code != http.StatusForbidden {
		t.Errorf("unserved bucket: status = %d", rec.Code)
	}
	traversal := strings.Replace(good, "run/work/ab/.command.sh", "run/work/ab/../../secrets", 1)
	if rec := do(t, h, http.MethodGet, traversal); rec.Code != http.StatusBadRequest {
		t.Errorf("traversal: status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, good); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d", rec.Code)
	}
	if len(p.calls) != 0 {
		t.Errorf("presigned on refusals: %v", p.calls)
	}
}

func TestEndedTasks(t *testing.T) {
	task := activeTask()
	gw, _, h := newGateway(t, task)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	gw.now = func() time.Time { return now }
	u := gw.URL("t1", "conquest", "run/work/ab/.exitcode")

	task.State = domain.StateComplete
	finished := now.Add(-5 * time.Minute)
	task.FinishedAt = &finished
	if rec := do(t, h, http.MethodPut, u); rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("within grace: status = %d", rec.Code)
	}
	finished = now.Add(-11 * time.Minute)
	if rec := do(t, h, http.MethodPut, u); rec.Code != http.StatusGone {
		t.Errorf("after grace: status = %d", rec.Code)
	}
}

func TestKeysWithSpecialCharacters(t *testing.T) {
	task := activeTask()
	task.TaskRequestPayload = json.RawMessage(`{"inputs": [{"url": "s3://conquest/run/a%20b/c%25d%23e.txt", "path": "/x"}]}`)
	gw, p, h := newGateway(t, task)

	rec := do(t, h, http.MethodGet, gw.URL("t1", "conquest", "run/a b/c%d#e.txt"))

	if rec.Code != http.StatusTemporaryRedirect || p.calls[0] != "GET conquest/run/a b/c%d#e.txt" {
		t.Fatalf("status = %d, presigned = %v", rec.Code, p.calls)
	}
}

func TestNewValidates(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	secret := []byte(strings.Repeat("s", 32))
	for name, cfg := range map[string]Config{
		"relative url": {PublicURL: "/storage", Secret: secret, Buckets: []string{"b"}},
		"short secret": {PublicURL: "https://x", Secret: []byte("short"), Buckets: []string{"b"}},
		"no bucket":    {PublicURL: "https://x", Secret: secret},
	} {
		if _, err := New(cfg, tasks{}, &presigner{}, log); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
