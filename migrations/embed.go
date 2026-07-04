// Package migrations embeds the SQL migration files into the binary via go:embed.
// Embedding means the migrations can never drift from the code that expects them,
// and deployment is a single self-contained artifact (no loose .sql to ship).
package migrations

import "embed"

// FS holds every .sql migration in this directory, read by golang-migrate's iofs
// source (see cmd/migrate).
//
//go:embed *.sql
var FS embed.FS
