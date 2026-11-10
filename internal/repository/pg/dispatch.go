package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/anuragxxd/conquest/internal/repository"
)

// ClaimForDispatch implements repository.DispatchQueue. FOR UPDATE SKIP
// LOCKED lets several dispatchers (server replicas) claim tasks at once
// without claiming the same task twice.
func (s *TaskStore) ClaimForDispatch(ctx context.Context, limit int, lease time.Duration) ([]repository.DispatchClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE tasks t SET
			dispatch_lease_until = NOW() + make_interval(secs => $2),
			dispatch_attempts = t.dispatch_attempts + 1
		FROM (
			SELECT task_id FROM tasks
			WHERE state = 'QUEUED' AND COALESCE(backend_ref, '') = ''
				AND (dispatch_lease_until IS NULL OR dispatch_lease_until < NOW())
				AND (dispatch_not_before IS NULL OR dispatch_not_before <= NOW())
			ORDER BY created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		) pending
		WHERE t.task_id = pending.task_id
		RETURNING t.task_id, t.dispatch_attempts`,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim tasks for dispatch: %w", err)
	}
	defer rows.Close()
	var claims []repository.DispatchClaim
	for rows.Next() {
		var c repository.DispatchClaim
		if err := rows.Scan(&c.TaskID, &c.Attempt); err != nil {
			return nil, fmt.Errorf("scan dispatch claim: %w", err)
		}
		claims = append(claims, c)
	}
	return claims, rows.Err()
}

// DeferDispatch implements repository.DispatchQueue.
func (s *TaskStore) DeferDispatch(ctx context.Context, taskID string, notBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tasks SET dispatch_lease_until = NULL, dispatch_not_before = $2
		WHERE task_id = $1 AND state = 'QUEUED'`,
		taskID, notBefore)
	if err != nil {
		return fmt.Errorf("defer dispatch of task %s: %w", taskID, err)
	}
	return nil
}
