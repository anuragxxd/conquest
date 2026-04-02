package wes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anuragxxd/conquest/internal/adapter"
	"github.com/anuragxxd/conquest/internal/controller"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
	"github.com/anuragxxd/conquest/internal/storage"
)

func TestGetServiceInfo_IncludesWDLWhenWDLEngineIsConfigured(t *testing.T) {
	ctrl := controller.NewRunController(
		&wesRunStoreStub{},
		&wesTaskStoreStub{},
		&wesEventStoreStub{},
		storage.Layout{Bucket: "conquest", Version: "v1"},
		adapter.NewRegistry(&wesAdapterStub{name: "wdl"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	handler := NewHandler(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/ga4gh/wes/v1/service-info", nil)
	resp := httptest.NewRecorder()
	handler.GetServiceInfo(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	var info ServiceInfo
	if err := json.Unmarshal(resp.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	versions, ok := info.WorkflowTypeVersions["WDL"]
	if !ok {
		t.Fatal("expected WDL workflow type in service-info")
	}
	if len(versions.WorkflowTypeVersion) != 2 || versions.WorkflowTypeVersion[0] != "1.0" || versions.WorkflowTypeVersion[1] != "1.1" {
		t.Fatalf("WDL versions = %v", versions.WorkflowTypeVersion)
	}
	if info.WorkflowEngineVersions["wdl"] != "1.12.1" {
		t.Fatalf("wdl engine version = %q", info.WorkflowEngineVersions["wdl"])
	}
}

type wesAdapterStub struct{ name string }

func (s *wesAdapterStub) Name() string { return s.name }

func (s *wesAdapterStub) PrepareRun(string, adapter.SubmissionRequest, storage.Layout) (*adapter.PreparedRun, error) {
	return nil, nil
}

type wesRunStoreStub struct{}

func (s *wesRunStoreStub) Create(context.Context, *domain.Run) error { return nil }
func (s *wesRunStoreStub) Get(context.Context, string) (*domain.Run, error) {
	return nil, nil
}
func (s *wesRunStoreStub) GetStatus(context.Context, string) (domain.State, error) {
	return domain.StateQueued, nil
}
func (s *wesRunStoreStub) List(context.Context, repository.Page) (*repository.PageResult[*domain.Run], error) {
	return &repository.PageResult[*domain.Run]{}, nil
}
func (s *wesRunStoreStub) UpdateState(context.Context, string, domain.State, domain.State) error {
	return nil
}
func (s *wesRunStoreStub) SetError(context.Context, string, domain.State, string) error { return nil }
func (s *wesRunStoreStub) CountByState(context.Context) (map[domain.State]int, error) {
	return map[domain.State]int{}, nil
}
func (s *wesRunStoreStub) ListActiveRuns(context.Context, int) ([]*domain.Run, error) {
	return nil, nil
}
func (s *wesRunStoreStub) SetBackendRef(context.Context, string, string) error { return nil }
func (s *wesRunStoreStub) SetBackendState(context.Context, string, json.RawMessage) error {
	return nil
}
func (s *wesRunStoreStub) SetWorkspaceBinding(context.Context, string, *domain.WorkspaceBinding) error {
	return nil
}

type wesTaskStoreStub struct{}

func (s *wesTaskStoreStub) Create(context.Context, *domain.Task) error { return nil }
func (s *wesTaskStoreStub) Get(context.Context, string) (*domain.Task, error) {
	return nil, nil
}
func (s *wesTaskStoreStub) GetByTESID(context.Context, string) (*domain.Task, error) {
	return nil, nil
}
func (s *wesTaskStoreStub) List(context.Context, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return &repository.PageResult[*domain.Task]{}, nil
}
func (s *wesTaskStoreStub) ListByRunID(context.Context, string, repository.Page) (*repository.PageResult[*domain.Task], error) {
	return &repository.PageResult[*domain.Task]{}, nil
}
func (s *wesTaskStoreStub) ListActiveTasks(context.Context, int) ([]*domain.Task, error) {
	return nil, nil
}
func (s *wesTaskStoreStub) UpdateState(context.Context, string, domain.State, domain.State) error {
	return nil
}
func (s *wesTaskStoreStub) SetExitCode(context.Context, string, int32) error    { return nil }
func (s *wesTaskStoreStub) SetBackendRef(context.Context, string, string) error { return nil }
func (s *wesTaskStoreStub) SetBackendState(context.Context, string, json.RawMessage) error {
	return nil
}
func (s *wesTaskStoreStub) SetOutputLogs(context.Context, string, json.RawMessage) error { return nil }

type wesEventStoreStub struct{}

func (s *wesEventStoreStub) Append(context.Context, *domain.Event) error { return nil }
func (s *wesEventStoreStub) ListByRunID(context.Context, string) ([]*domain.Event, error) {
	return nil, nil
}
