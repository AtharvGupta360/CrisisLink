// Command migrate applies (or rolls back) the embedded SQL migrations against the
// database in DATABASE_URL. Kept as a SEPARATE binary from the API server so
// schema changes are an explicit, auditable step in deploy/CI — the API process
// never silently mutates the schema on boot.
//
// Usage:
//
//	DATABASE_URL=postgres://... go run ./cmd/migrate up     # apply pending (default)
//	DATABASE_URL=postgres://... go run ./cmd/migrate down   # roll everything back
//
// Libraries (used, not reinvented):
//   - golang-migrate: versioned migration runner; tracks current version in a
//     schema_migrations table it manages inside the DB.
//   - pgx/v5/stdlib: registers pgx as a database/sql driver named "pgx" so
//     golang-migrate's postgres driver can talk to it.
package main

import (
	"database/sql"
	"errors"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // side-effect: registers the "pgx" sql driver

	"github.com/AtharvGupta360/CrisisLink/migrations"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	direction := "up"
	if len(os.Args) > 1 {
		direction = os.Args[1]
	}

	// Source: the embedded *.sql files. "." is the FS root where they live.
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		log.Fatalf("open migration source: %v", err)
	}

	// Open a database/sql handle via the pgx stdlib driver, then wrap it in
	// golang-migrate's postgres driver.
	sqlDB, err := sql.Open("pgx", dsn)
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

	// ErrNoChange = already at the target version; that's success, not failure.
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migrate %s failed: %v", direction, err)
	}

	log.Printf("migrate %s: done", direction)
}
