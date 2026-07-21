package audit

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
)

// Writer returns the side effect of auditing one event: append a row.
//
// It is an outbox.TxFunc, not a plain method, so it CANNOT run outside the inbox's
// transaction. That is what makes auditing exactly-once: the dedup marker and the
// audit row commit together, so a redelivered message can never produce a second
// entry, and a crash mid-write leaves neither.
//
// ON CONFLICT DO NOTHING on event_id is a second, independent guard. The dedup
// ledger already prevents duplicates; this makes the audit table's own uniqueness
// true regardless of who writes to it or whether that ledger is ever reset.
func Writer(e outbox.EventEnvelope) outbox.TxFunc {
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at)
			 VALUES ($1, $2, $3, $4::uuid, $5, $6)
			 ON CONFLICT (event_id) DO NOTHING`,
			e.ID, e.EventType, e.AggregateType, e.AggregateID, e.Payload, e.OccurredAt,
		)
		return err
	}
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// List returns audit entries newest-first, optionally filtered to one aggregate —
// "show me everything that ever happened to this dispatch".
func (r *Repository) List(ctx context.Context, aggregateType, aggregateID string, limit int) ([]Entry, error) {
	const q = `
		SELECT id, event_id, event_type, aggregate_type, aggregate_id::text, payload, occurred_at, recorded_at
		FROM audit_log
		WHERE ($1 = '' OR aggregate_type = $1)
		  AND ($2 = '' OR aggregate_id = $2::uuid)
		ORDER BY id DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, q, aggregateType, aggregateID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.EventID, &e.EventType, &e.AggregateType,
			&e.AggregateID, &e.Payload, &e.OccurredAt, &e.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
