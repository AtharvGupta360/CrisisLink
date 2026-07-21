// Package relay drains the transactional outbox to a message broker. It is the
// second half of the outbox pattern (P19 writes events; this publishes them). It
// runs as its own process (cmd/relay), sharing only the DB and Kafka with the API.
package relay

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
)

// Publisher sends one serialized event to the broker. Abstracted so the relay is
// broker-agnostic and testable; KafkaPublisher is the real implementation.
type Publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
}

// Relay polls the outbox on an interval and publishes unpublished events.
type Relay struct {
	outbox   *outbox.OutboxRepository
	pub      Publisher
	interval time.Duration
	batch    int
	log      *zap.SugaredLogger
}

func New(outbox *outbox.OutboxRepository, pub Publisher, interval time.Duration, batch int, log *zap.SugaredLogger) *Relay {
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

// drain publishes batches until nothing is due (or ctx stops). Each batch:
// publish to Kafka, then mark published — the order that makes it at-least-once
// (see OutboxRepository.PublishBatch).
func (r *Relay) drain(ctx context.Context) {
	for {
		res, err := r.outbox.PublishBatch(ctx, r.batch, func(e outbox.OutboxEvent) error {
			value, merr := json.Marshal(outbox.EventEnvelope{
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
		// Only INFRASTRUCTURE errors (the database) stop the loop. A publish failure
		// is not returned here — it has already been recorded on its own row with a
		// backoff, so the loop must keep going rather than let one bad event stall
		// the drain.
		if err != nil {
			r.log.Errorw("relay batch failed", "error", err)
			return
		}
		if res.Published > 0 {
			r.log.Infof("relay published %d event(s)", res.Published)
		}
		if res.Failed > 0 {
			r.log.Warnw("relay had publish failures, they are backed off for retry",
				"failed", res.Failed, "deadLettered", res.DeadLettered)
		}
		if res.DeadLettered > 0 {
			// Loud on purpose: this is the "a human must look at this" signal.
			r.log.Errorw("events exhausted their retry budget and were DEAD-LETTERED",
				"count", res.DeadLettered, "maxAttempts", outbox.MaxPublishAttempts)
		}
		// Stop when the batch came back short: everything currently DUE is handled.
		// Rows in backoff are deliberately not counted — they become due later, and
		// a later tick will pick them up.
		if res.Claimed < r.batch {
			return
		}
	}
}
