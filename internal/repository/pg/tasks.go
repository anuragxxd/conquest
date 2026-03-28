package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

// TaskStore implements repository.TaskStore backed by Postgres.
type TaskStore struct {
	pool *pgxpool.Pool
}

// NewTaskStore creates a new TaskStore.
func NewTaskStore(pool *pgxpool.Pool) *TaskStore {
	return &TaskStore{pool: pool}
}

func (s *TaskStore) Create(ctx context.Context, task *domain.Task) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tasks (
			task_id, run_id, engine_task_name, tes_task_id,
			state, resources, task_request_payload, backend_ref, backend_state,
			output_logs,
			created_at, updated_at, started_at, finished_at,
			log_location, exit_code, attempt
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		task.TaskID, task.RunID, task.EngineTaskName, task.TESTaskID,
		string(task.State), task.Resources, task.TaskRequestPayload,
		task.BackendRef, task.BackendState,
		task.OutputLogs,
		task.CreatedAt, task.UpdatedAt, task.StartedAt, task.FinishedAt,
		task.LogLocation, task.ExitCode, task.Attempt,
	)
	return err
}

// taskColumns is the canonical column list for SELECT queries on tasks.
const taskColumns = `task_id, run_id, engine_task_name, tes_task_id,
	state, resources, task_request_payload, backend_ref, backend_state,
	output_logs,
	created_at, updated_at, started_at, finished_at,
	log_location, exit_code, attempt`

func (s *TaskStore) Get(ctx context.Context, taskID string) (*domain.Task, error) {
	return s.scanTask(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE task_id = $1`, taskID)
}

func (s *TaskStore) GetByTESID(ctx context.Context, tesTaskID string) (*domain.Task, error) {
	return s.scanTask(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE tes_task_id = $1`, tesTaskID)
}

func (s *TaskStore) List(ctx context.Context, page repository.Page) (*repository.PageResult[*domain.Task], error) {
	limit := page.Size
	if limit <= 0 {
		limit = 50
	}

	var rows pgx.Rows
	var err error

	if page.Token == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+taskColumns+` FROM tasks
			ORDER BY created_at DESC, task_id DESC
			LIMIT $1`, limit+1)
	} else {
		cursorTime, cursorID, parseErr := parseCursor(page.Token)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid page token: %w", parseErr)
		}
		rows, err = s.pool.Query(ctx,
			`SELECT `+taskColumns+` FROM tasks
			WHERE (created_at, task_id) < ($1, $2)
			ORDER BY created_at DESC, task_id DESC
			LIMIT $3`, cursorTime, cursorID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*domain.Task
	for rows.Next() {
		t, err := scanTaskFromRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	result := &repository.PageResult[*domain.Task]{}
	if len(tasks) > limit {
		last := tasks[limit-1]
		result.Items = tasks[:limit]
		result.NextToken = fmt.Sprintf("%d:%s", last.CreatedAt.UnixNano(), last.TaskID)
	} else {
		result.Items = tasks
	}
	return result, nil
}

// ListActiveTasks returns all tasks in non-terminal states. Used by the reconciler.
func (s *TaskStore) ListActiveTasks(ctx context.Context, limit int) ([]*domain.Task, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+taskColumns+` FROM tasks
		WHERE state NOT IN ('COMPLETE','EXECUTOR_ERROR','SYSTEM_ERROR','CANCELED','PREEMPTED')
		ORDER BY created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*domain.Task
	for rows.Next() {
		t, err := scanTaskFromRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// SetBackendState updates the backend-specific state blob for a task.
func (s *TaskStore) SetBackendState(ctx context.Context, taskID string, state json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tasks SET backend_state = $1, updated_at = NOW() WHERE task_id = $2`,
		state, taskID)
	return err
}

func (s *TaskStore) ListByRunID(ctx context.Context, runID string, page repository.Page) (*repository.PageResult[*domain.Task], error) {
	limit := page.Size
	if limit <= 0 {
		limit = 50
	}

	var rows pgx.Rows
	var err error

	if page.Token == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+taskColumns+` FROM tasks
			WHERE run_id = $1
			ORDER BY created_at ASC, task_id ASC
			LIMIT $2`, runID, limit+1)
	} else {
		cursorTime, cursorID, parseErr := parseCursor(page.Token)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid page token: %w", parseErr)
		}
		rows, err = s.pool.Query(ctx,
			`SELECT `+taskColumns+` FROM tasks
			WHERE run_id = $1 AND (created_at, task_id) > ($2, $3)
			ORDER BY created_at ASC, task_id ASC
			LIMIT $4`, runID, cursorTime, cursorID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("list tasks by run: %w", err)
	}
	defer rows.Close()

	var tasks []*domain.Task
	for rows.Next() {
		t, err := scanTaskFromRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	result := &repository.PageResult[*domain.Task]{}
	if len(tasks) > limit {
		last := tasks[limit-1]
		result.Items = tasks[:limit]
		result.NextToken = fmt.Sprintf("%d:%s", last.CreatedAt.UnixNano(), last.TaskID)
	} else {
		result.Items = tasks
	}
	return result, nil
}

func (s *TaskStore) UpdateState(ctx context.Context, taskID string, from, to domain.State) error {
	// Set started_at on first INITIALIZING, finished_at on first terminal state.
	toState := domain.State(to)
	var tag pgconn.CommandTag
	var err error
	if toState == domain.StateInitializing {
		tag, err = s.pool.Exec(ctx, `
			UPDATE tasks SET state = $1,
				updated_at = NOW(),
				started_at = COALESCE(started_at, NOW())
			WHERE task_id = $2 AND state = $3`,
			string(to), taskID, string(from))
	} else if toState.IsTerminal() {
		tag, err = s.pool.Exec(ctx, `
			UPDATE tasks SET state = $1,
				updated_at = NOW(),
				finished_at = COALESCE(finished_at, NOW())
			WHERE task_id = $2 AND state = $3`,
			string(to), taskID, string(from))
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE tasks SET state = $1,
				updated_at = NOW()
			WHERE task_id = $2 AND state = $3`,
			string(to), taskID, string(from))
	}
	if err != nil {
		return fmt.Errorf("update task state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (s *TaskStore) SetExitCode(ctx context.Context, taskID string, code int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tasks SET exit_code = $1, updated_at = NOW() WHERE task_id = $2`,
		code, taskID)
	return err
}

func (s *TaskStore) SetBackendRef(ctx context.Context, taskID string, ref string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tasks SET backend_ref = $1, updated_at = NOW() WHERE task_id = $2`,
		ref, taskID)
	return err
}

func (s *TaskStore) SetOutputLogs(ctx context.Context, taskID string, logs json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tasks SET output_logs = $1, updated_at = NOW() WHERE task_id = $2`,
		logs, taskID)
	return err
}

func (s *TaskStore) scanTask(ctx context.Context, query string, args ...any) (*domain.Task, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	t, err := scanTaskFields(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}
	return t, nil
}

// scannable is satisfied by both pgx.Row and pgx.Rows.
type scannable interface {
	Scan(dest ...any) error
}

func scanTaskFields(s scannable) (*domain.Task, error) {
	t := &domain.Task{}
	var stateStr string
	var resources, requestPayload, backendState, outputLogs []byte
	err := s.Scan(
		&t.TaskID, &t.RunID, &t.EngineTaskName, &t.TESTaskID,
		&stateStr, &resources, &requestPayload, &t.BackendRef, &backendState,
		&outputLogs,
		&t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.FinishedAt,
		&t.LogLocation, &t.ExitCode, &t.Attempt,
	)
	if err != nil {
		return nil, err
	}
	t.State = domain.State(stateStr)
	if resources != nil {
		t.Resources = json.RawMessage(resources)
	}
	if requestPayload != nil {
		t.TaskRequestPayload = json.RawMessage(requestPayload)
	}
	if backendState != nil {
		t.BackendState = json.RawMessage(backendState)
	}
	if outputLogs != nil {
		t.OutputLogs = json.RawMessage(outputLogs)
	}
	return t, nil
}

func scanTaskFromRow(rows pgx.Rows) (*domain.Task, error) {
	t, err := scanTaskFields(rows)
	if err != nil {
		return nil, fmt.Errorf("scan task row: %w", err)
	}
	return t, nil
}
