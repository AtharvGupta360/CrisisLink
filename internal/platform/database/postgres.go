// Package database owns the Postgres connection (pgx pool) and the migration
// runner wiring. NewPostgresConnection returns a *pgxpool.Pool that all
// repositories share.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
)

// NewPostgresConnection builds the pool from config and verifies connectivity
// with a Ping, so an unreachable DB fails FAST at boot rather than on the first
// query. Caller owns Close().
func NewPostgresConnection(cfg *config.DatabaseConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MaxConnLifetimeMinutes > 0 {
		pc.MaxConnLifetime = time.Duration(cfg.MaxConnLifetimeMinutes) * time.Minute
	}
	if cfg.MaxConnIdleMinutes > 0 {
		pc.MaxConnIdleTime = time.Duration(cfg.MaxConnIdleMinutes) * time.Minute
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
