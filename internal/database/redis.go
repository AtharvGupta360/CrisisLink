package database

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AtharvGupta360/CrisisLink/internal/config"
)

// NewRedisConnection builds the Redis client and verifies connectivity with a
// Ping, so an unreachable Redis fails FAST at boot (same policy as Postgres)
// rather than surprising us on the first request. Caller owns Close().
//
// Redis holds state that must be SHARED ACROSS PROCESSES — starting with the rate
// limiter's per-IP token buckets, which were previously an in-process map and so
// were wrong the moment you ran more than one API replica.
func NewRedisConnection(cfg *config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
