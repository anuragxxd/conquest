package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

func submitTask() *domain.Task {
	task := domain.NewTask("run-1")
	task.TESTaskID = "tes-1"
	task.TaskRequestPayload = json.RawMessage(`{"executors":[{"image":"ubuntu:24.04","command":["true"]}]}`)
	return task
}

func submitBackend(client *fake.Clientset) *Backend {
	return NewBackend(client, Config{Namespace: "conquest", ConquestImage: "conquest:test", S3Bucket: "conquest"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A dispatcher retries a submission whose Job creation timed out; if the
// API created the Job anyway, the retry adopts it instead of failing.
func TestSubmitAdoptsTheTasksExistingJob(t *testing.T) {
	existing := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "conquest-task-tes-1", Namespace: "conquest",
		Labels: map[string]string{LabelTaskID: "tes-1"},
	}}
	task := submitTask()

	if err := submitBackend(fake.NewSimpleClientset(existing)).Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.BackendRef != "conquest-task-tes-1" {
		t.Fatalf("BackendRef = %q", task.BackendRef)
	}
}

func TestSubmitRefusesAnotherTasksJob(t *testing.T) {
	other := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "conquest-task-tes-1", Namespace: "conquest",
		Labels: map[string]string{LabelTaskID: "someone-else"},
	}}

	err := submitBackend(fake.NewSimpleClientset(other)).Submit(context.Background(), submitTask())
	if err == nil || backend.IsRetryable(err) {
		t.Fatalf("err = %v, want a non-retryable error", err)
	}
}

func TestFailedJobCreationIsRetryable(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("etcdserver: request timed out")
	})

	err := submitBackend(client).Submit(context.Background(), submitTask())
	if !backend.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable", err)
	}
}
