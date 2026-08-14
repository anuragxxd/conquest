package domain

import (
	"encoding/json"
	"time"
)

// EventType identifies the kind of domain event.
type EventType string

const (
	EventRunCreated         EventType = "run.created"
	EventRunStateChanged    EventType = "run.state_changed"
	EventRunCancelRequested EventType = "run.cancel_requested"
	EventRunFailed          EventType = "run.failed"
	EventRunCompleted       EventType = "run.completed"

	EventTaskCreated      EventType = "task.created"
	EventTaskStateChanged EventType = "task.state_changed"
	EventTaskFailed       EventType = "task.failed"
	EventTaskCompleted    EventType = "task.completed"
)

// Event records a domain-level occurrence. Events form an append-only log
// per run, enabling audit trails and eventual event-driven processing.
type Event struct {
	EventID   string          `json:"event_id"`
	RunID     string          `json:"run_id"`
	TaskID    string          `json:"task_id,omitempty"` // empty for run-level events
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// NewEvent creates an Event with a fresh UUIDv7 ID and the current timestamp.
func NewEvent(runID, taskID string, eventType EventType, payload json.RawMessage) *Event {
	return &Event{
		EventID:   NewID(),
		RunID:     runID,
		TaskID:    taskID,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}

// StateChangePayload is the standard payload for state-change events.
type StateChangePayload struct {
	FromState State  `json:"from_state"`
	ToState   State  `json:"to_state"`
	Reason    string `json:"reason,omitempty"`
}
