// Command migrate applies (up) or rolls back (down) the embedded SQL migrations.
// Kept SEPARATE from the API server so schema change is an explicit, auditable
// deploy step — the API never mutates schema on boot.
//
// Usage (run from the repo root so config.yaml is found):
//
//	go run ./cmd/migrate up     # apply pending (default)
//	go run ./cmd/migrate down   # roll everything back
package main

import (
	"database/sql"
	"errors"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/database/migrations"
)

func main() {
	cfg, err := config.LoadConfig(".")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	direction := "up"
	if len(os.Args) > 1 {
		direction = os.Args[1]
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		log.Fatalf("open migration source: %v", err)
	}

	sqlDB, err := sql.Open("pgx", cfg.Database.DSN())
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	drv, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		log.Fatalf("init migrate driver: %v", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		log.Fatalf("init migrator: %v", err)
	}

	switch direction {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	default:
		log.Fatalf("unknown direction %q (use \"up\" or \"down\")", direction)
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migrate %s failed: %v", direction, err)
	}
	log.Printf("migrate %s: done", direction)
}
