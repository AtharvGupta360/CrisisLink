package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

// NotificationWriter returns the side effect of consuming an event: append a
// notification. It's a TxFunc (not a plain method) so it can only run inside the
// inbox's transaction, alongside the dedup marker — that's what makes processing
// exactly-once.
func NotificationWriter(eventID int64, eventType, message string) TxFunc {
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO notifications (event_id, event_type, message) VALUES ($1, $2, $3)`,
			eventID, eventType, message,
		)
		return err
	}
}

type NotificationRepository struct {
	pool *pgxpool.Pool
}

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

// ListRecent returns the newest notifications — the visible proof of what the
// consumer did (and that it did it only once per event).
func (r *NotificationRepository) ListRecent(ctx context.Context, limit int) ([]models.Notification, error) {
	const q = `SELECT id, event_id, event_type, message, created_at
	           FROM notifications ORDER BY id DESC LIMIT $1`
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Notification, 0, limit)
	for rows.Next() {
		var n models.Notification
		if err := rows.Scan(&n.ID, &n.EventID, &n.EventType, &n.Message, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
