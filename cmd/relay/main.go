// Command relay is the outbox publisher: a separate process that polls
// outbox_events for unpublished rows and ships them to Kafka, then stamps
// published_at. Kept separate from the API so it can be scaled/deployed on its own
// (it shares only the DB and Kafka). Run it alongside cmd/server.
//
//	go run ./cmd/relay
package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/database"
	"github.com/AtharvGupta360/CrisisLink/internal/relay"
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

	outbox := outbox.NewOutboxRepository(pool)

	// Make sure the topic exists before we start publishing.
	if err := relay.EnsureTopic(cfg.Kafka.Brokers, cfg.Kafka.Topic, 1); err != nil {
		common.Logger.Fatalf("ensure kafka topic: %v", err)
	}
	pub := relay.NewKafkaPublisher(cfg.Kafka.Brokers, cfg.Kafka.Topic)
	defer pub.Close()

	r := relay.New(outbox, pub, 1*time.Second, 100, common.Logger)

	// Graceful stop: cancel the poll loop on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The relay is its own process, so it needs its own scrape target — Prometheus
	// cannot see these counters through the API's endpoint. Started after ctx exists
	// so it shuts down with everything else.
	go relay.ServeMetrics(ctx, ":9101", common.Logger)

	common.Logger.Infof("outbox relay starting (topic=%s brokers=%v)", cfg.Kafka.Topic, cfg.Kafka.Brokers)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		common.Logger.Fatalf("relay: %v", err)
	}
	common.Logger.Info("outbox relay stopped")
}
