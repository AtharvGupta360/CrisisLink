package models

import (
	"encoding/json"
	"time"
)

// Aggregate types (which kind of entity an event is about).
const (
	AggregateDispatch = "dispatch"
	AggregateVictim   = "victim"
)

// Event types. Namespaced <aggregate>.<past-tense-fact> — events describe things
// that already happened.
const (
	EventDispatchCreated = "dispatch.created"
	EventVictimAssigned  = "victim.assigned"
)

// OutboxEvent is one row of the transactional outbox. It is written in the same
// transaction as the domain change it describes, then relayed to Kafka (P20).
type OutboxEvent struct {
	ID            int64           `json:"id"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	EventType     string          `json:"eventType"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
	PublishedAt   *time.Time      `json:"publishedAt"` // nil until relayed
}
