// Package relay drains the transactional outbox to a message broker. It is the
// second half of the outbox pattern (P19 writes events; this publishes them). It
// runs as its own process (cmd/relay), sharing only the DB and Kafka with the API.
package relay

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// Publisher sends one serialized event to the broker. Abstracted so the relay is
// broker-agnostic and testable; KafkaPublisher is the real implementation.
type Publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
}

// envelope is the on-the-wire shape of a published event.
type envelope struct {
	ID            int64           `json:"id"`
	EventType     string          `json:"eventType"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurredAt"`
}

// Relay polls the outbox on an interval and publishes unpublished events.
type Relay struct {
	outbox   *repository.OutboxRepository
	pub      Publisher
	interval time.Duration
	batch    int
	log      *zap.SugaredLogger
}

func New(outbox *repository.OutboxRepository, pub Publisher, interval time.Duration, batch int, log *zap.SugaredLogger) *Relay {
	return &Relay{outbox: outbox, pub: pub, interval: interval, batch: batch, log: log}
}

// Run polls until ctx is cancelled (graceful stop on SIGINT/SIGTERM).
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.log.Infof("relay running: interval=%s batch=%d", r.interval, r.batch)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.drain(ctx)
		}
	}
}

// drain publishes batches until the outbox is empty (or an error / ctx stop). Each
// batch: publish to Kafka, then mark published — the order that makes it
// at-least-once (see OutboxRepository.PublishBatch).
func (r *Relay) drain(ctx context.Context) {
	for {
		n, err := r.outbox.PublishBatch(ctx, r.batch, func(e models.OutboxEvent) error {
			value, merr := json.Marshal(envelope{
				ID:            e.ID,
				EventType:     e.EventType,
				AggregateType: e.AggregateType,
				AggregateID:   e.AggregateID,
				Payload:       e.Payload,
				OccurredAt:    e.CreatedAt,
			})
			if merr != nil {
				return merr
			}
			// Key by aggregate id → same aggregate's events share a partition
			// (per-aggregate ordering preserved).
			return r.pub.Publish(ctx, e.AggregateID, value)
		})
		if n > 0 {
			r.log.Infof("relay published %d event(s)", n)
		}
		if err != nil {
			r.log.Errorw("relay publish batch failed", "error", err)
			return
		}
		if n < r.batch {
			return // last batch wasn't full → outbox drained for now
		}
	}
}
