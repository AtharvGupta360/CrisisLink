// Package consumer reads domain events off Kafka and processes them IDEMPOTENTLY.
// Kafka gives at-least-once delivery (the relay may republish after a crash), so
// the same event can arrive twice; the inbox dedup table makes the side effect
// happen exactly once. Runs as its own process (cmd/consumer).
package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// Consumer reads the event topic as part of a Kafka consumer group.
type Consumer struct {
	reader *kafka.Reader
	inbox  *repository.InboxRepository
	name   string // scopes the dedup ledger: (consumer, event_id)
	log    *zap.SugaredLogger
}

func New(brokers []string, topic, groupID string, inbox *repository.InboxRepository, log *zap.SugaredLogger) *Consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID, // group => offset tracking + partition assignment
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	return &Consumer{reader: r, inbox: inbox, name: groupID, log: log}
}

func (c *Consumer) Close() error { return c.reader.Close() }

// Run consumes until ctx is cancelled.
//
// FetchMessage does NOT commit the offset — we commit only AFTER the message is
// successfully processed. That's at-least-once on the consume side: a crash before
// the commit means redelivery (safe, because processing is idempotent). Committing
// first would instead LOSE the event on a crash.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Infof("consumer running: group=%s", c.name)
	for {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			return err // ctx cancelled or reader closed
		}

		if err := c.handle(ctx, m); err != nil {
			// Don't commit: the message will be redelivered and retried.
			c.log.Errorw("processing failed; offset NOT committed (will retry)", "error", err)
			continue
		}

		if err := c.reader.CommitMessages(ctx, m); err != nil {
			c.log.Errorw("commit offset failed", "error", err)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, m kafka.Message) error {
	var e models.EventEnvelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		// Poison message: it will never parse, so don't block the partition forever.
		c.log.Errorw("undecodable message, skipping", "error", err, "offset", m.Offset)
		return nil
	}

	// The dedup marker + the notification commit together (see InboxRepository).
	processed, err := c.inbox.ProcessOnce(ctx, c.name, e.ID,
		repository.NotificationWriter(e.ID, e.EventType, notificationMessage(e)),
	)
	if err != nil {
		return err
	}

	if processed {
		c.log.Infof("processed event id=%d type=%s", e.ID, e.EventType)
	} else {
		c.log.Infof("DUPLICATE event id=%d type=%s — skipped (already processed)", e.ID, e.EventType)
	}
	return nil
}

func notificationMessage(e models.EventEnvelope) string {
	switch e.EventType {
	case models.EventDispatchCreated:
		return fmt.Sprintf("Unit dispatched (dispatch %s)", e.AggregateID)
	case models.EventVictimAssigned:
		return fmt.Sprintf("Victim %s assigned to a shelter", e.AggregateID)
	default:
		return fmt.Sprintf("%s (%s)", e.EventType, e.AggregateID)
	}
}
