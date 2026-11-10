package tes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

type memTasks struct {
	repository.TaskStore // unused methods panic
	tasks                map[string]*domain.Task
}

func (m *memTasks) Create(_ context.Context, t *domain.Task) error {
	m.tasks[t.TaskID] = t
	return nil
}

func (m *memTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	if t, ok := m.tasks[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}

func (m *memTasks) UpdateState(_ context.Context, id string, from, to domain.State) error {
	if m.tasks[id].State != from {
		return errors.New("concurrent update")
	}
	m.tasks[id].State = to
	return nil
}

type memEvents struct{ repository.EventStore }

func (memEvents) Append(context.Context, *domain.Event) error { return nil }

type countingBackend struct{ submits, cancels int }

func (b *countingBackend) Submit(context.Context, *domain.Task) error { b.submits++; return nil }
func (b *countingBackend) Cancel(context.Context, *domain.Task) error { b.cancels++; return nil }
func (b *countingBackend) Refresh(context.Context, *domain.Task) (*backend.BackendStatus, error) {
	return nil, nil
}

type notifier struct{ n int }

func (n *notifier) Notify() { n.n++ }

func TestAsyncCreateStoresTheTaskQueuedAndNotifies(t *testing.T) {
	tasks, be, disp := &memTasks{tasks: map[string]*domain.Task{}}, &countingBackend{}, &notifier{}
	h := NewHandler(nil, tasks, memEvents{}, be)
	h.SetDispatcher(disp)
	req := &Task{Executors: []Executor{{Image: "ubuntu:24.04", Command: []string{"true"}}}}
	w := httptest.NewRecorder()

	h.createAndSubmitTask(w, httptest.NewRequest(http.MethodPost, "/ga4gh/tes/v1/tasks", nil), req, "run-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var resp CreateTaskResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	task := tasks.tasks[resp.ID]
	if task == nil || task.State != domain.StateQueued || task.BackendRef != "" {
		t.Fatalf("task = %+v, want QUEUED and undispatched", task)
	}
	if be.submits != 0 || disp.n != 1 {
		t.Fatalf("submits = %d, notifications = %d; want 0 and 1", be.submits, disp.n)
	}
}

func TestCancellingAnUndispatchedTaskSkipsTheBackend(t *testing.T) {
	task := domain.NewTask("run-1")
	task.TESTaskID = task.TaskID
	tasks, be := &memTasks{tasks: map[string]*domain.Task{task.TaskID: task}}, &countingBackend{}
	h := NewHandler(nil, tasks, memEvents{}, be)
	h.SetDispatcher(&notifier{})
	r := chi.NewRouter()
	h.Routes(r)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ga4gh/tes/v1/tasks/"+task.TaskID+":cancel", strings.NewReader("{}")))

	if w.Code != http.StatusOK || task.State != domain.StateCanceled {
		t.Fatalf("status = %d, state = %s: %s", w.Code, task.State, w.Body)
	}
	if be.cancels != 0 {
		t.Fatalf("backend Cancel called %d times for a task no backend has", be.cancels)
	}
}
