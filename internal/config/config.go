// Package config loads all runtime settings from environment variables into a
// single typed struct at startup. Two deliberate rules live here:
//
//  1. Config comes from the ENVIRONMENT, never a file checked into git. The same
//     binary must run unchanged in dev / CI / prod, and secrets (DB password,
//     JWT key) must never touch the repo.
//
//  2. We FAIL FAST. A missing/invalid required setting kills the process at boot,
//     loudly — not as a nil-pointer panic on the first request during an actual
//     disaster. "Crash at startup" is a feature.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the whole application's settings, resolved once at boot and then
// treated as read-only.
type Config struct {
	// HTTPAddr is the host:port the API server binds to, e.g. ":8080".
	HTTPAddr string

	// ShutdownTimeout bounds how long we wait for in-flight requests to drain on
	// SIGTERM before forcing exit. See cmd/crisislink/main.go.
	ShutdownTimeout time.Duration

	// DatabaseURL is the Postgres DSN, e.g.
	//   postgres://user:pass@localhost:5432/crisislink?sslmode=disable
	// REQUIRED — the process refuses to start without it (fail-fast).
	DatabaseURL string

	// DBMaxConns is the pool ceiling. Budget = Postgres max_connections / app
	// instances, with headroom. Too low: requests queue for a free conn. Too
	// high: we exhaust Postgres's process limit and it rejects connections.
	DBMaxConns int32

	// DBMaxConnLifetime retires a connection after this long even if healthy —
	// lets load rebalance after a failover and dodges server-side memory creep.
	DBMaxConnLifetime time.Duration

	// DBMaxConnIdleTime closes connections idle this long so a spike doesn't pin
	// idle connections open forever.
	DBMaxConnIdleTime time.Duration
}

// Load reads the environment and returns a validated Config, or an error the
// caller (main) turns into a boot-time crash.
func Load() (Config, error) {
	dbURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:          envOr("HTTP_ADDR", ":8080"),
		ShutdownTimeout:   15 * time.Second,
		DatabaseURL:       dbURL,
		DBMaxConns:        int32(envIntOr("DB_MAX_CONNS", 10)),
		DBMaxConnLifetime: 60 * time.Minute,
		DBMaxConnIdleTime: 5 * time.Minute,
	}

	// Optional override; if set but garbage, crash now rather than ignore intent.
	if raw, ok := os.LookupEnv("SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT %q is not a valid duration (want e.g. \"15s\"): %w", raw, err)
		}
		cfg.ShutdownTimeout = d
	}

	return cfg, nil
}

// requireEnv returns the value or an error if the var is unset/empty. This is
// the fail-fast primitive: a genuinely required setting must never silently
// default.
func requireEnv(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return v, nil
}

// envOr returns the env var's value, or fallback if unset or empty. Treating ""
// as unset avoids an exported-but-empty variable silently overriding a default.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envIntOr returns the env var parsed as int, or fallback if unset/empty/invalid.
func envIntOr(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
