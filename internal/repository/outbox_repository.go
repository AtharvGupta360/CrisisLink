package repository

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

// writeOutbox inserts an event into the outbox within the CALLER'S transaction.
// That is the whole point of the pattern: the event row commits atomically with
// the domain change, so they can never disagree. It takes a pgx.Tx (not the pool)
// precisely so it joins an in-progress transaction. Shared by the repositories in
// this package (dispatch, victim) that emit events.
func writeOutbox(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, payload any) error {
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

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

const outboxColumns = `id, aggregate_type, aggregate_id::text, event_type, payload, created_at, published_at`

func scanOutbox(s scanner, e *models.OutboxEvent) error {
	return s.Scan(
		&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType,
		&e.Payload, &e.CreatedAt, &e.PublishedAt,
	)
}

// ListRecent returns the most recent outbox events (published or not), newest
// first — an ops view onto the event stream.
func (r *OutboxRepository) ListRecent(ctx context.Context, limit int) ([]models.OutboxEvent, error) {
	const q = `SELECT ` + outboxColumns + ` FROM outbox_events ORDER BY id DESC LIMIT $1`
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.OutboxEvent, 0, limit)
	for rows.Next() {
		var e models.OutboxEvent
		if err := scanOutbox(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
