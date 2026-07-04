// Package db constructs the shared Postgres connection pool (pgx/v5). It is a
// low-level infrastructure helper: it deliberately does NOT import any internal
// module or the config package — the caller (cmd/*) passes primitives in. That
// keeps the dependency direction clean (modules depend on db, db depends on
// nothing of ours) so nothing here blocks a future service extraction.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is the subset of settings this package needs. Defined here (not imported
// from internal/config) so pkg/db stays independent; main maps config.Config to
// this.
type Config struct {
	DSN             string        // Postgres connection string
	MaxConns        int32         // pool ceiling
	MaxConnLifetime time.Duration // retire conns after this long
	MaxConnIdleTime time.Duration // close conns idle this long
}

// New builds a *pgxpool.Pool and verifies connectivity before returning, so a
// bad DSN or an unreachable database fails FAST at boot rather than on the first
// query. The returned pool is safe for concurrent use; the caller owns Close().
func New(ctx context.Context, c Config) (*pgxpool.Pool, error) {
	// ParseConfig reads the DSN (and any pool params embedded in it). We then
	// override the knobs explicitly so behaviour is driven by our config, not by
	// whatever happens to be in the URL.
	pc, err := pgxpool.ParseConfig(c.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	if c.MaxConns > 0 {
		pc.MaxConns = c.MaxConns
	}
	if c.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = c.MaxConnLifetime
	}
	if c.MaxConnIdleTime > 0 {
		pc.MaxConnIdleTime = c.MaxConnIdleTime
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// pgxpool is lazy — NewWithConfig does not actually open a connection. Ping
	// forces one now so an unreachable DB is caught at startup, not later.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close() // don't leak the pool we just created
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
