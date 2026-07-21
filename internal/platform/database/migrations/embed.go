// Package migrations embeds the SQL migration files into the binary so they can
// never drift from the code and deployment is a single artifact. Read by
// golang-migrate's iofs source (see cmd/migrate).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
