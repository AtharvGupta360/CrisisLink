// Command consumer reads domain events from Kafka and processes them idempotently
// (inbox dedup), turning at-least-once delivery into exactly-once processing. A
// separate process from the API and the relay.
//
//	go run ./cmd/consumer
package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"sync"
	"syscall"

	"github.com/AtharvGupta360/CrisisLink/internal/audit"
	"github.com/AtharvGupta360/CrisisLink/internal/consumer"
	"github.com/AtharvGupta360/CrisisLink/internal/notification"
	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/database"
	"github.com/AtharvGupta360/CrisisLink/internal/relay"
)

// Consumer group names. Each names both a Kafka consumer group AND the dedup
// ledger scope in processed_events, so the two groups process every event
// independently: the auditor recording an event does not stop the notifier from
// also recording it, and either can be replayed alone.
const (
	notifierGroup = "crisislink-notifier"
	auditorGroup  = "crisislink-auditor"
)

func main() {
	cfg, err := config.LoadConfig(".")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	common.InitLogger(cfg.Server.Mode)
	defer common.Logger.Sync()

	pool, err := database.NewPostgresConnection(&cfg.Database)
	if err != nil {
		common.Logger.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	inbox := outbox.NewInboxRepository(pool)

	// Make sure both topics exist before we read/write them.
	for _, t := range []string{cfg.Kafka.Topic, cfg.Kafka.DLQTopic} {
		if err := relay.EnsureTopic(cfg.Kafka.Brokers, t, 1); err != nil {
			common.Logger.Fatalf("ensure kafka topic %s: %v", t, err)
		}
	}

	// Messages that exhaust their retries (or can't be decoded) go here instead of
	// blocking the partition forever.
	dlq := relay.NewKafkaPublisher(cfg.Kafka.Brokers, cfg.Kafka.DLQTopic)
	defer dlq.Close()

	// TWO consumer groups over the SAME topic. Kafka groups are independent
	// subscriptions — each gets its own copy of every message and its own offsets —
	// so adding the auditor is purely additive and cannot slow the notifier down.
	notifier := consumer.New(cfg.Kafka.Brokers, cfg.Kafka.Topic, notifierGroup, inbox, dlq,
		func(e outbox.EventEnvelope) outbox.TxFunc {
			return notification.NotificationWriter(e.ID, e.EventType, consumer.NotificationMessage(e))
		}, common.Logger)
	defer notifier.Close()

	auditor := consumer.New(cfg.Kafka.Brokers, cfg.Kafka.Topic, auditorGroup, inbox, dlq,
		audit.Writer, common.Logger)
	defer auditor.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	common.Logger.Infof("consumers starting (topic=%s groups=[%s %s])",
		cfg.Kafka.Topic, notifierGroup, auditorGroup)

	// Run both groups concurrently. WaitGroup, not a bare `go`, so shutdown waits
	// for each to finish its in-flight message and commit its offset — otherwise a
	// SIGTERM could drop work that would then be redelivered (correct, thanks to the
	// dedup ledger, but needlessly noisy).
	var wg sync.WaitGroup
	for _, c := range []*consumer.Consumer{notifier, auditor} {
		wg.Add(1)
		go func(c *consumer.Consumer) {
			defer wg.Done()
			if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				common.Logger.Errorf("consumer: %v", err)
			}
		}(c)
	}
	wg.Wait()
	common.Logger.Info("consumer stopped")
}
