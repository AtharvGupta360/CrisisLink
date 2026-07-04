// Package config loads all runtime settings from environment variables into a
// single typed struct at startup. Two deliberate rules live here:
//
//  1. Config comes from the ENVIRONMENT, never a file checked into git. The same
//     binary must run unchanged in dev / CI / prod, and secrets (DB password,
//     JWT key) must never touch the repo. Docker Compose (P2) and k8s (P30)
//     both inject env naturally.
//
//  2. We FAIL FAST. A bad or missing required setting kills the process at boot,
//     loudly — not as a nil-pointer panic on the first request during an actual
//     disaster. "Crash at startup" is a feature, not a bug.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config is the whole application's settings, resolved once at boot and then
// treated as read-only. Later phases add DatabaseURL (P3), Redis/Kafka (P2+),
// and the JWT signing key (P4) here.
type Config struct {
	// HTTPAddr is the host:port the API server binds to, e.g. ":8080".
	HTTPAddr string

	// ShutdownTimeout bounds how long we wait for in-flight requests to drain
	// on SIGTERM before forcing the process to exit. See cmd/crisislink/main.go.
	// Too short: we cut off real work. Too long: a hung request holds a deploy
	// hostage. 15s is a common default that outlasts a normal request but not a
	// wedged one.
	ShutdownTimeout time.Duration
}

// Load reads the environment and returns a validated Config, or an error that
// the caller (main) turns into a boot-time crash. It returns an error rather
// than panicking so the failure path is explicit and testable.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:        envOr("HTTP_ADDR", ":8080"),
		ShutdownTimeout: 15 * time.Second,
	}

	// SHUTDOWN_TIMEOUT is optional — but if the operator bothered to set it and
	// typed garbage, we crash now instead of silently ignoring their intent.
	// This is the fail-fast principle in miniature until P3 gives us a genuinely
	// required setting (DATABASE_URL).
	if raw, ok := os.LookupEnv("SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT %q is not a valid duration (want e.g. \"15s\"): %w", raw, err)
		}
		cfg.ShutdownTimeout = d
	}

	return cfg, nil
}

// envOr returns the env var's value, or fallback if it is unset or empty.
// Treating "" as unset avoids the classic bug where an exported-but-empty
// variable silently overrides a sensible default.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
