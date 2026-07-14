// Command server is the single entry point (composition root) for CrisisLink.
// It bootstraps infrastructure in order — config -> logger -> DB pool -> HTTP
// server — and hands dependencies to the server layer. Resource cleanup happens
// on the way out via defers (pool closed after the server has drained).
package main

import (
	"log"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/config"
	"github.com/AtharvGupta360/CrisisLink/internal/database"
	"github.com/AtharvGupta360/CrisisLink/internal/server"
)

func main() {
	// Config first. Uses stdlib log here because the logger isn't up yet.
	cfg, err := config.LoadConfig(".")
	if err != nil {
		log.Fatalf("config load error: %v", err)
	}

	common.InitLogger(cfg.Server.Mode)
	defer common.Logger.Sync() //nolint:errcheck // best-effort flush on exit
	common.Logger.Infow("starting crisislink", "port", cfg.Server.Port, "mode", cfg.Server.Mode)

	// DB pool: Ping-on-boot means an unreachable DB crashes us here, not on the
	// first request. Closed LAST (deferred now) so it outlives the server drain.
	pool, err := database.NewPostgresConnection(&cfg.Database)
	if err != nil {
		common.Logger.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()
	common.Logger.Info("database pool ready")

	// Redis: shared, cross-replica state (rate-limit token buckets). Ping-on-boot
	// for the same fail-fast reason as the DB.
	rdb, err := database.NewRedisConnection(&cfg.Redis)
	if err != nil {
		common.Logger.Fatalf("redis connection failed: %v", err)
	}
	defer rdb.Close()
	common.Logger.Info("redis ready")

	router := server.NewServer(cfg, pool, rdb)
	if err := server.Run(router, cfg); err != nil {
		common.Logger.Errorf("server exited with error: %v", err)
	}
	common.Logger.Info("shutdown complete")
}
