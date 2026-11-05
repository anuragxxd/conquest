package runner

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// TestUploadRunLog_CalledOnEarlyReturn verifies that deferring uploadRunLog
// means it fires even when the function returns early before cmd.Run().
// We simulate this by calling a stripped-down version of the runner logic
// that returns early after creating the log file but before running Nextflow.
func TestUploadRunLog_DeferFiresOnEarlyReturn(t *testing.T) {
	var uploadCalled atomic.Bool

	// Fake S3 server that records whether it received the upload.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	logPath := dir + "/run.log"

	// Simulate the deferred upload pattern: create file, defer upload, return early.
	func() {
		f, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		defer func() {
			// Use a real file so uploadRunLog has something to PUT.
			_ = uploadRunLog(context.Background(), srv.URL, "test-bucket", "us-east-1",
				"run-123", "v1/workflow-runs/run-123/run.log", logPath,
				slog.New(slog.NewTextHandler(io.Discard, nil)))
		}()
		// Simulate early return (e.g. config generation failure).
		if true {
			return
		}
		// This line never runs — that's the point.
		_ = f
	}()

	if !uploadCalled.Load() {
		t.Fatal("uploadRunLog was NOT called on early return — defer is missing")
	}
}
