package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anuragxxd/conquest/internal/domain"
)

// EventStore implements repository.EventStore backed by Postgres.
type EventStore struct {
	pool *pgxpool.Pool
}

// NewEventStore creates a new EventStore.
func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{pool: pool}
}

func (s *EventStore) Append(ctx context.Context, event *domain.Event) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO events (event_id, run_id, task_id, type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		event.EventID, event.RunID, nilIfEmpty(event.TaskID),
		string(event.Type), event.Payload, event.CreatedAt,
	)
	return err
}

func (s *EventStore) ListByRunID(ctx context.Context, runID string) ([]*domain.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, run_id, task_id, type, payload, created_at
		FROM events
		WHERE run_id = $1
		ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []*domain.Event
	for rows.Next() {
		e := &domain.Event{}
		var eventType string
		var payload []byte
		var taskID *string
		if err := rows.Scan(&e.EventID, &e.RunID, &taskID, &eventType, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Type = domain.EventType(eventType)
		if payload != nil {
			e.Payload = json.RawMessage(payload)
		}
		if taskID != nil {
			e.TaskID = *taskID
		}
		events = append(events, e)
	}
	return events, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
