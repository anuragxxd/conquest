// Package repository defines storage interfaces for the domain entities.
//
// Implementations live in sub-packages (e.g. repository/pg for Postgres).
// The interfaces are small and focused on what the controller layer needs.
package repository

import (
	"context"
	"encoding/json"

	"github.com/anuragxxd/conquest/internal/domain"
)

// Page holds pagination parameters.
type Page struct {
	Size  int
	Token string // opaque cursor; empty for first page
}

// PageResult holds a page of results with a next-page cursor.
type PageResult[T any] struct {
	Items     []T
	NextToken string // empty if no more pages
}

// RunStore persists and queries WES runs.
type RunStore interface {
	Create(ctx context.Context, run *domain.Run) error
	Get(ctx context.Context, runID string) (*domain.Run, error)
	GetStatus(ctx context.Context, runID string) (domain.State, error)
	List(ctx context.Context, page Page) (*PageResult[*domain.Run], error)
	UpdateState(ctx context.Context, runID string, from, to domain.State) error
	SetError(ctx context.Context, runID string, to domain.State, summary string) error
	CountByState(ctx context.Context) (map[domain.State]int, error)
	// Batch D additions:
	ListActiveRuns(ctx context.Context, limit int) ([]*domain.Run, error)
	SetBackendRef(ctx context.Context, runID string, ref string) error
	SetBackendState(ctx context.Context, runID string, state json.RawMessage) error
}

// TaskStore persists and queries TES tasks.
type TaskStore interface {
	Create(ctx context.Context, task *domain.Task) error
	Get(ctx context.Context, taskID string) (*domain.Task, error)
	GetByTESID(ctx context.Context, tesTaskID string) (*domain.Task, error)
	List(ctx context.Context, page Page) (*PageResult[*domain.Task], error)
	ListByRunID(ctx context.Context, runID string, page Page) (*PageResult[*domain.Task], error)
	ListActiveTasks(ctx context.Context, limit int) ([]*domain.Task, error)
	UpdateState(ctx context.Context, taskID string, from, to domain.State) error
	SetExitCode(ctx context.Context, taskID string, code int32) error
	SetBackendRef(ctx context.Context, taskID string, ref string) error
	SetBackendState(ctx context.Context, taskID string, state json.RawMessage) error
	SetOutputLogs(ctx context.Context, taskID string, logs json.RawMessage) error
}

// EventStore persists and queries domain events.
type EventStore interface {
	Append(ctx context.Context, event *domain.Event) error
	ListByRunID(ctx context.Context, runID string) ([]*domain.Event, error)
}
