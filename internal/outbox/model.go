package outbox

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
	EventDispatchCreated   = "dispatch.created"
	EventDispatchCompleted = "dispatch.completed"
	EventVictimAssigned    = "victim.assigned"
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

	// Retry/dead-letter state. Attempts counts failed publishes; NextAttemptAt is
	// the backoff gate the relay filters on; DeadAt is set once the retry budget is
	// exhausted, taking the row out of circulation permanently.
	Attempts      int        `json:"attempts"`
	LastError     *string    `json:"lastError,omitempty"`
	NextAttemptAt time.Time  `json:"nextAttemptAt"`
	DeadAt        *time.Time `json:"deadAt,omitempty"`
}

// EventEnvelope is the on-the-wire shape of a published event (relay -> Kafka ->
// consumers). Defined here so producer and consumer share one contract.
//
// ID is the outbox event id and doubles as the IDEMPOTENCY KEY: it stays the same
// across redeliveries, whereas a Kafka offset would differ for a republished event.
type EventEnvelope struct {
	ID            int64           `json:"id"`
	EventType     string          `json:"eventType"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurredAt"`
}
