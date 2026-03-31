package adapter

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/storage"
)

type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error: %s: %s", e.Field, e.Msg)
}

type SubmissionRequest struct {
	WorkflowType             string
	WorkflowTypeVersion      string
	WorkflowURL              string
	WorkflowParams           json.RawMessage
	WorkflowEngine           string
	WorkflowEngineVersion    string
	WorkflowEngineParameters json.RawMessage
	Tags                     map[string]string
}

type PreparedRun struct {
	WorkflowEngine        string
	WorkflowEngineVersion string
	RequestPayload        json.RawMessage
	ResolvedWorkflowRef   string
	RunnerSpec            backend.RunnerSpec
}

type RunAdapter interface {
	Name() string
	PrepareRun(runID string, req SubmissionRequest, layout storage.Layout) (*PreparedRun, error)
}

type Registry struct {
	adapters map[string]RunAdapter
}

func NewRegistry(adapters ...RunAdapter) *Registry {
	r := &Registry{adapters: make(map[string]RunAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		r.adapters[adapter.Name()] = adapter
	}
	return r
}

func (r *Registry) Get(name string) (RunAdapter, bool) {
	if r == nil {
		return nil, false
	}
	adapter, ok := r.adapters[strings.TrimSpace(name)]
	return adapter, ok
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func InferEngine(workflowType, workflowEngine string) string {
	if engine := strings.TrimSpace(workflowEngine); engine != "" {
		return engine
	}

	switch strings.TrimSpace(workflowType) {
	case "NFL", "nextflow":
		return "nextflow"
	case "SMK", "snakemake":
		return "snakemake"
	case "WDL", "wdl":
		return "cromwell"
	default:
		return strings.TrimSpace(workflowType)
	}
}
