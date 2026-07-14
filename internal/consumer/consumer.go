// Package consumer reads domain events off Kafka and processes them IDEMPOTENTLY.
// Kafka delivers at-least-once (the relay may republish after a crash), so the same
// event can arrive twice; the inbox dedup table makes the side effect happen exactly
// once. Failing messages get bounded retries and then go to a dead-letter topic, so
// one bad message can never block a partition. Runs as its own process (cmd/consumer).
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

const (
	// maxAttempts bounds in-process retries before a message is dead-lettered.
	maxAttempts = 3
	// baseBackoff is multiplied by the attempt number (linear backoff).
	baseBackoff = 500 * time.Millisecond
)

// Publisher sends a message to a topic (used here for the dead-letter queue).
type Publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
}

// Consumer reads the event topic as part of a Kafka consumer group.
type Consumer struct {
	reader *kafka.Reader
	inbox  *repository.InboxRepository
	dlq    Publisher // where poison / repeatedly-failing messages go
	name   string    // scopes the dedup ledger: (consumer, event_id)
	log    *zap.SugaredLogger
}

func New(brokers []string, topic, groupID string, inbox *repository.InboxRepository, dlq Publisher, log *zap.SugaredLogger) *Consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID, // group => offset tracking + partition assignment
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	return &Consumer{reader: r, inbox: inbox, dlq: dlq, name: groupID, log: log}
}

func (c *Consumer) Close() error { return c.reader.Close() }

// Run consumes until ctx is cancelled.
//
// FetchMessage does NOT commit the offset — we commit only AFTER the message is
// dealt with (processed, deduped, or dead-lettered). That's at-least-once on the
// consume side: a crash before the commit means redelivery, which is safe because
// processing is idempotent. Committing first would instead LOSE the event.
//
// Crucially we ALWAYS end up committing: a message that keeps failing is
// dead-lettered rather than retried forever, so it can never wedge the partition.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Infof("consumer running: group=%s", c.name)
	for {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			return err // ctx cancelled or reader closed
		}

		c.process(ctx, m)

		if err := c.reader.CommitMessages(ctx, m); err != nil {
			c.log.Errorw("commit offset failed", "error", err)
		}
	}
}

// process handles one message: decode, then retry the idempotent side effect up to
// maxAttempts, then dead-letter if it still won't go through.
func (c *Consumer) process(ctx context.Context, m kafka.Message) {
	var e models.EventEnvelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		// PERMANENT failure: this will never parse, so retrying is pointless.
		// Straight to the DLQ, then commit and keep the partition moving.
		c.deadLetter(ctx, m, "undecodable message: "+err.Error())
		return
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// The dedup marker + the notification commit together (see InboxRepository).
		processed, err := c.inbox.ProcessOnce(ctx, c.name, e.ID,
			repository.NotificationWriter(e.ID, e.EventType, notificationMessage(e)),
		)
		if err == nil {
			if processed {
				c.log.Infof("processed event id=%d type=%s", e.ID, e.EventType)
			} else {
				c.log.Infof("DUPLICATE event id=%d type=%s — skipped (already processed)", e.ID, e.EventType)
			}
			return
		}

		lastErr = err
		c.log.Warnw("processing attempt failed",
			"eventId", e.ID, "attempt", attempt, "maxAttempts", maxAttempts, "error", err)
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * baseBackoff):
			}
		}
	}

	// Retries exhausted (e.g. a persistent DB error). Dead-letter it so the
	// partition keeps flowing; the message is preserved for inspection/replay.
	c.deadLetter(ctx, m, fmt.Sprintf("failed after %d attempts: %v", maxAttempts, lastErr))
}

// deadLetter republishes the raw message to the DLQ topic. The original bytes are
// preserved so the event can be replayed after the bug is fixed.
func (c *Consumer) deadLetter(ctx context.Context, m kafka.Message, reason string) {
	if c.dlq == nil {
		c.log.Errorw("no DLQ configured; dropping message", "reason", reason, "offset", m.Offset)
		return
	}
	if err := c.dlq.Publish(ctx, string(m.Key), m.Value); err != nil {
		c.log.Errorw("DLQ publish failed", "error", err, "reason", reason)
		return
	}
	c.log.Warnw("event DEAD-LETTERED", "reason", reason, "offset", m.Offset)
}

func notificationMessage(e models.EventEnvelope) string {
	switch e.EventType {
	case models.EventDispatchCreated:
		return fmt.Sprintf("Unit dispatched (dispatch %s)", e.AggregateID)
	case models.EventDispatchCompleted:
		return fmt.Sprintf("Dispatch %s completed", e.AggregateID)
	case models.EventVictimAssigned:
		return fmt.Sprintf("Victim %s assigned to a shelter", e.AggregateID)
	default:
		return fmt.Sprintf("%s (%s)", e.EventType, e.AggregateID)
	}
}
