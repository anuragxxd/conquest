// Package pg implements the repository interfaces using pgx/v5.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/repository"
)

// RunStore implements repository.RunStore backed by Postgres.
type RunStore struct {
	pool *pgxpool.Pool
}

// NewRunStore creates a new RunStore.
func NewRunStore(pool *pgxpool.Pool) *RunStore {
	return &RunStore{pool: pool}
}

func (s *RunStore) Create(ctx context.Context, run *domain.Run) error {
	wsKind, wsClaim, wsAccess, wsPV := flattenWorkspaceBinding(run.Workspace)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runs (
			run_id, workflow_type, workflow_type_version,
			workflow_engine, workflow_engine_version,
			state, request_payload, resolved_workflow_ref,
			output_prefix, layout_version, run_root_uri,
			backend_ref, backend_state,
			created_at, updated_at, started_at, finished_at, error_summary,
			workspace_kind, workspace_claim_name, workspace_access_mode,
			workspace_pv_name
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		run.RunID, run.WorkflowType, run.WorkflowTypeVersion,
		run.WorkflowEngine, run.WorkflowEngineVersion,
		string(run.State), run.RequestPayload, run.ResolvedWorkflowRef,
		run.OutputPrefix, run.LayoutVersion, run.RunRootURI,
		run.BackendRef, run.BackendState,
		run.CreatedAt, run.UpdatedAt, run.StartedAt, run.FinishedAt, run.ErrorSummary,
		wsKind, wsClaim, wsAccess, wsPV,
	)
	return err
}

// runColumns is the canonical column list for SELECT queries on runs.
const runColumns = `run_id, workflow_type, workflow_type_version,
	workflow_engine, workflow_engine_version,
	state, request_payload, resolved_workflow_ref,
	output_prefix, layout_version, run_root_uri,
	backend_ref, backend_state,
	created_at, updated_at, started_at, finished_at, error_summary,
	workspace_kind, workspace_claim_name, workspace_access_mode,
	workspace_pv_name`

// scanRunFields scans a row into a Run using the canonical column order.
func scanRunFields(s interface{ Scan(dest ...any) error }) (*domain.Run, error) {
	r := &domain.Run{}
	var stateStr string
	var payload, backendState []byte
	var wsKind, wsClaim, wsAccess, wsPV *string
	err := s.Scan(
		&r.RunID, &r.WorkflowType, &r.WorkflowTypeVersion,
		&r.WorkflowEngine, &r.WorkflowEngineVersion,
		&stateStr, &payload, &r.ResolvedWorkflowRef,
		&r.OutputPrefix, &r.LayoutVersion, &r.RunRootURI,
		&r.BackendRef, &backendState,
		&r.CreatedAt, &r.UpdatedAt, &r.StartedAt, &r.FinishedAt, &r.ErrorSummary,
		&wsKind, &wsClaim, &wsAccess, &wsPV,
	)
	if err != nil {
		return nil, err
	}
	r.State = domain.State(stateStr)
	if payload != nil {
		r.RequestPayload = json.RawMessage(payload)
	}
	if backendState != nil {
		r.BackendState = json.RawMessage(backendState)
	}
	r.Workspace = inflateWorkspaceBinding(wsKind, wsClaim, wsAccess, wsPV)
	return r, nil
}

func (s *RunStore) Get(ctx context.Context, runID string) (*domain.Run, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE run_id = $1`, runID)
	r, err := scanRunFields(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", runID, err)
	}
	return r, nil
}

func (s *RunStore) GetStatus(ctx context.Context, runID string) (domain.State, error) {
	var stateStr string
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM runs WHERE run_id = $1`, runID,
	).Scan(&stateStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return domain.State(stateStr), nil
}

func (s *RunStore) List(ctx context.Context, page repository.Page) (*repository.PageResult[*domain.Run], error) {
	limit := page.Size
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error

	if page.Token == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+runColumns+` FROM runs
			ORDER BY created_at DESC, run_id DESC
			LIMIT $1`, limit+1)
	} else {
		cursorTime, cursorID, parseErr := parseCursor(page.Token)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid page token: %w", parseErr)
		}
		rows, err = s.pool.Query(ctx,
			`SELECT `+runColumns+` FROM runs
			WHERE (created_at, run_id) < ($1, $2)
			ORDER BY created_at DESC, run_id DESC
			LIMIT $3`, cursorTime, cursorID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []*domain.Run
	for rows.Next() {
		r, err := scanRunFields(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}

	result := &repository.PageResult[*domain.Run]{}
	if len(runs) > limit {
		last := runs[limit-1]
		result.Items = runs[:limit]
		result.NextToken = fmt.Sprintf("%d:%s", last.CreatedAt.UnixNano(), last.RunID)
	} else {
		result.Items = runs
	}
	return result, nil
}

func (s *RunStore) UpdateState(ctx context.Context, runID string, from, to domain.State) error {
	// Set started_at on first INITIALIZING, finished_at on first terminal state.
	toState := domain.State(to)
	var tag pgconn.CommandTag
	var err error
	if toState == domain.StateInitializing {
		tag, err = s.pool.Exec(ctx, `
			UPDATE runs SET state = $1,
				updated_at = NOW(),
				started_at = COALESCE(started_at, NOW())
			WHERE run_id = $2 AND state = $3`,
			string(to), runID, string(from))
	} else if toState.IsTerminal() {
		tag, err = s.pool.Exec(ctx, `
			UPDATE runs SET state = $1,
				updated_at = NOW(),
				finished_at = COALESCE(finished_at, NOW())
			WHERE run_id = $2 AND state = $3`,
			string(to), runID, string(from))
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE runs SET state = $1,
				updated_at = NOW()
			WHERE run_id = $2 AND state = $3`,
			string(to), runID, string(from))
	}
	if err != nil {
		return fmt.Errorf("update run state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

// ListActiveRuns returns all runs in non-terminal states. Used by the run reconciler.
func (s *RunStore) ListActiveRuns(ctx context.Context, limit int) ([]*domain.Run, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM runs
		WHERE state NOT IN ('COMPLETE','EXECUTOR_ERROR','SYSTEM_ERROR','CANCELED','PREEMPTED')
		  AND backend_ref IS NOT NULL
		ORDER BY created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active runs: %w", err)
	}
	defer rows.Close()

	var runs []*domain.Run
	for rows.Next() {
		r, err := scanRunFields(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func (s *RunStore) SetBackendRef(ctx context.Context, runID string, ref string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE runs SET backend_ref = $1, updated_at = NOW() WHERE run_id = $2`,
		ref, runID)
	return err
}

func (s *RunStore) SetBackendState(ctx context.Context, runID string, state json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE runs SET backend_state = $1, updated_at = NOW() WHERE run_id = $2`,
		state, runID)
	return err
}

func (s *RunStore) SetError(ctx context.Context, runID string, to domain.State, summary string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET state = $1, error_summary = $2, updated_at = NOW(), finished_at = NOW()
		WHERE run_id = $3 AND state NOT IN ('COMPLETE','EXECUTOR_ERROR','SYSTEM_ERROR','CANCELED','PREEMPTED')`,
		string(to), summary, runID)
	if err != nil {
		return fmt.Errorf("set run error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (s *RunStore) CountByState(ctx context.Context) (map[domain.State]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT state, COUNT(*) FROM runs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[domain.State]int)
	for rows.Next() {
		var stateStr string
		var count int
		if err := rows.Scan(&stateStr, &count); err != nil {
			return nil, err
		}
		counts[domain.State(stateStr)] = count
	}
	return counts, nil
}

func (s *RunStore) SetWorkspaceBinding(ctx context.Context, runID string, binding *domain.WorkspaceBinding) error {
	wsKind, wsClaim, wsAccess, wsPV := flattenWorkspaceBinding(binding)
	_, err := s.pool.Exec(ctx, `
		UPDATE runs SET workspace_kind = $1, workspace_claim_name = $2,
			workspace_access_mode = $3, workspace_pv_name = $4, updated_at = NOW()
		WHERE run_id = $5`,
		wsKind, wsClaim, wsAccess, wsPV, runID)
	return err
}

// flattenWorkspaceBinding converts a WorkspaceBinding pointer to nullable SQL column values.
func flattenWorkspaceBinding(w *domain.WorkspaceBinding) (kind, claimName, accessMode, pvName *string) {
	if w == nil {
		return nil, nil, nil, nil
	}
	k := string(w.Kind)
	kind = &k
	if w.ClaimName != "" {
		claimName = &w.ClaimName
	}
	if w.AccessMode != "" {
		a := string(w.AccessMode)
		accessMode = &a
	}
	if w.PVName != "" {
		pvName = &w.PVName
	}
	return kind, claimName, accessMode, pvName
}

// inflateWorkspaceBinding reconstructs a WorkspaceBinding from nullable SQL column values.
// Returns nil if kind is NULL (emptydir runs before the migration).
func inflateWorkspaceBinding(kind, claimName, accessMode, pvName *string) *domain.WorkspaceBinding {
	if kind == nil || *kind == "" || *kind == string(domain.WorkspaceKindNone) {
		return nil
	}
	w := &domain.WorkspaceBinding{Kind: domain.WorkspaceKind(*kind)}
	if claimName != nil {
		w.ClaimName = *claimName
	}
	if accessMode != nil {
		w.AccessMode = domain.WorkspaceAccessMode(*accessMode)
	}
	if pvName != nil {
		w.PVName = *pvName
	}
	return w
}

func parseCursor(token string) (unixNano int64, runID string, err error) {
	// Token format: "unixNano:runID"
	for i := 0; i < len(token); i++ {
		if token[i] == ':' {
			unixNano, err = strconv.ParseInt(token[:i], 10, 64)
			if err != nil {
				return 0, "", err
			}
			runID = token[i+1:]
			return unixNano, runID, nil
		}
	}
	return 0, "", fmt.Errorf("malformed cursor %q", token)
}
