// Package audit records an immutable trail of every domain event.
//
// It is built by its OWN Kafka consumer group, independent of the notifier. Groups
// are separate subscriptions: each receives every message and tracks its own
// offsets, so the auditor can be added, replayed, or removed without affecting
// notifications — and a slow auditor cannot hold them up.
package audit

import (
	"encoding/json"
	"time"
)

// Entry is one immutable audit record.
//
// OccurredAt is when the event happened; RecordedAt is when the auditor saw it.
// Keeping both is what lets someone distinguish "this action took two hours" from
// "this action took two hours to be written down" — a distinction that matters when
// reconstructing a timeline after an incident.
type Entry struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurredAt"`
	RecordedAt    time.Time       `json:"recordedAt"`
}
