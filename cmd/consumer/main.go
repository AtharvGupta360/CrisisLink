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
	"syscall"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/config"
	"github.com/AtharvGupta360/CrisisLink/internal/consumer"
	"github.com/AtharvGupta360/CrisisLink/internal/database"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// consumerGroup names both the Kafka consumer group and the dedup ledger scope.
const consumerGroup = "crisislink-notifier"

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

	inbox := repository.NewInboxRepository(pool)
	c := consumer.New(cfg.Kafka.Brokers, cfg.Kafka.Topic, consumerGroup, inbox, common.Logger)
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	common.Logger.Infof("consumer starting (topic=%s group=%s)", cfg.Kafka.Topic, consumerGroup)
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		common.Logger.Errorf("consumer: %v", err)
	}
	common.Logger.Info("consumer stopped")
}
