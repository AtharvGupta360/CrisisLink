package outbox

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

// WriteTx inserts an event into the outbox within the CALLER'S transaction.
// That is the whole point of the pattern: the event row commits atomically with
// the domain change, so they can never disagree — no dual write, no lost event,
// no phantom event. It takes a pgx.Tx (not the pool) precisely so it joins an
// in-progress transaction.
//
// This is the third module SEAM, and the most important one. It was package-private
// while dispatch, victim and outbox all shared one repository package; now that each
// owns its own module it is exported and consumed through an EventWriter interface,
// so an emitting module depends on the CAPABILITY to record an event, not on the
// outbox table itself.
func (r *OutboxRepository) WriteTx(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
		 VALUES ($1, $2::uuid, $3, $4)`,
		aggregateType, aggregateID, eventType, data,
	)
	return err
}

const outboxColumns = `id, aggregate_type, aggregate_id::text, event_type, payload, created_at, published_at`

func scanOutbox(s dbx.Scanner, e *OutboxEvent) error {
	return s.Scan(
		&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType,
		&e.Payload, &e.CreatedAt, &e.PublishedAt,
	)
}

// PublishBatch drains up to `limit` unpublished events: it claims a batch with
// FOR UPDATE SKIP LOCKED (so multiple relay workers grab disjoint batches and
// never double-publish), calls publish() for each, then stamps published_at on the
// ones that succeeded — all in one transaction.
//
// Ordering is deliberate: publish FIRST, mark AFTER (the mark commits last). A
// crash after publishing but before commit leaves the row unpublished → it will be
// republished (at-least-once). Marking before publishing would instead LOSE events.
// If publish() fails mid-batch, we still commit the ones already published (partial
// progress) and stop; the rest are retried next tick.
func (r *OutboxRepository) PublishBatch(ctx context.Context, limit int, publish func(OutboxEvent) error) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Claim a batch. SKIP LOCKED = don't wait on rows another worker holds.
	rows, err := tx.Query(ctx,
		`SELECT `+outboxColumns+` FROM outbox_events
		 WHERE published_at IS NULL
		 ORDER BY id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	events := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var e OutboxEvent
		if err := scanOutbox(rows, &e); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Publish first, collect the ids that made it. Stop at the first failure.
	publishedIDs := make([]int64, 0, len(events))
	var pubErr error
	for i := range events {
		if pubErr = publish(events[i]); pubErr != nil {
			break
		}
		publishedIDs = append(publishedIDs, events[i].ID)
	}

	if len(publishedIDs) > 0 {
		if _, err = tx.Exec(ctx,
			`UPDATE outbox_events SET published_at = now() WHERE id = ANY($1)`, publishedIDs,
		); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(publishedIDs), pubErr
}

// ListRecent returns the most recent outbox events (published or not), newest
// first — an ops view onto the event stream.
func (r *OutboxRepository) ListRecent(ctx context.Context, limit int) ([]OutboxEvent, error) {
	const q = `SELECT ` + outboxColumns + ` FROM outbox_events ORDER BY id DESC LIMIT $1`
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var e OutboxEvent
		if err := scanOutbox(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
