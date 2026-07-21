package outbox

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TxFunc is a side effect that MUST run inside a caller-supplied transaction. The
// signature exists to make the same-transaction requirement impossible to ignore:
// you cannot run the side effect without the tx that also writes the dedup marker.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// InboxRepository implements the consumer-side dedup (inbox) — the mirror of the
// outbox. It turns Kafka's at-least-once DELIVERY into exactly-once PROCESSING.
type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

// ProcessOnce runs sideEffect exactly once per (consumer, eventID), no matter how
// many times the event is delivered. Returns false if it was already processed.
//
// The whole correctness argument is that the dedup marker and the side effect share
// ONE transaction:
//   - marker in its own txn, then side effect  -> crash between = effect LOST forever
//     (marked done, never done)
//   - side effect, then marker in its own txn  -> crash between = effect DUPLICATED
//   - both in one txn (this)                   -> either both commit or neither; the
//     marker is proof the effect happened
//
// ON CONFLICT DO NOTHING makes the claim race-free: if a concurrent consumer already
// inserted the marker, we change zero rows and skip.
func (r *InboxRepository) ProcessOnce(ctx context.Context, consumer string, eventID int64, sideEffect TxFunc) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Claim the event. Zero rows affected => someone (or a previous delivery)
	// already processed it => this is a duplicate; do nothing.
	ct, err := tx.Exec(ctx,
		`INSERT INTO processed_events (consumer, event_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		consumer, eventID,
	)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, nil // duplicate — already processed
	}

	// First time: do the work in the SAME transaction as the marker. If this fails,
	// the rollback removes the marker too, so the event will be retried (not lost).
	if err := sideEffect(ctx, tx); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
