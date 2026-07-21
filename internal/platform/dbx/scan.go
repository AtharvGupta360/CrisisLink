// Package dbx holds database helpers that are shared by every domain module and
// belong to no single one of them.
//
// It exists because of the modular split: each module now owns its own repository
// in its own package, but they all need the same tiny scan abstraction. Putting it
// here keeps it in ONE place without making any module import another module just
// to borrow a type — which would be a boundary violation for a three-line interface.
package dbx

// Scanner is satisfied by both pgx.Row (from QueryRow) and pgx.Rows (from a Query
// loop), so a module can write ONE scan helper that serves both single-row and
// multi-row reads instead of duplicating it per call shape.
type Scanner interface {
	Scan(dest ...any) error
}
