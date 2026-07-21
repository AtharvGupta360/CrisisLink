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

const outboxColumns = `id, aggregate_type, aggregate_id::text, event_type, payload, created_at, published_at,
	attempts, last_error, next_attempt_at, dead_at`

func scanOutbox(s dbx.Scanner, e *OutboxEvent) error {
	return s.Scan(
		&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType,
		&e.Payload, &e.CreatedAt, &e.PublishedAt,
		&e.Attempts, &e.LastError, &e.NextAttemptAt, &e.DeadAt,
	)
}

// MaxPublishAttempts is the retry budget before an event is dead-lettered.
//
// It is bounded on purpose. Unbounded retry is indistinguishable from a hang: an
// event that can never publish would be retried until someone notices, and the
// backoff would grow until it effectively never runs again anyway. Giving up
// explicitly turns an invisible stall into a visible, queryable failure.
const MaxPublishAttempts = 5

// maxBackoffSeconds caps exponential growth. Without a cap, attempt 20 would
// schedule a retry ~12 days out — the event is not dead, but nobody would ever
// find out it recovered.
const maxBackoffSeconds = 300

// PublishResult reports what one batch did. Failures are NOT errors: a publish
// failure is an expected, recorded outcome, not something that should abort the
// drain loop.
type PublishResult struct {
	Claimed      int
	Published    int
	Failed       int
	DeadLettered int
}

// PublishBatch drains up to `limit` due events.
//
// Claiming uses FOR UPDATE SKIP LOCKED so multiple relay workers grab DISJOINT
// batches and never double-publish the same row — that is what lets the relay scale
// horizontally.
//
// Ordering is deliberate: publish FIRST, mark AFTER. A crash after publishing but
// before commit leaves the row unpublished, so it is republished — at-least-once,
// which the consumer's dedup absorbs. Marking first would instead LOSE events.
//
// The critical change from the naive version: a publish failure does NOT break the
// loop. It records the failure on THAT row (attempt count, error, backoff) and
// CONTINUES with the rest of the batch. Combined with the next_attempt_at filter,
// a poison row takes itself out of the selection window instead of sitting at the
// head of the queue starving every event behind it.
func (r *OutboxRepository) PublishBatch(ctx context.Context, limit int, publish func(OutboxEvent) error) (PublishResult, error) {
	var res PublishResult

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	// Claim only rows that are unpublished, not dead, and past their backoff gate.
	rows, err := tx.Query(ctx,
		`SELECT `+outboxColumns+` FROM outbox_events
		 WHERE published_at IS NULL
		   AND dead_at IS NULL
		   AND next_attempt_at <= now()
		 ORDER BY id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return res, err
	}
	events := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var e OutboxEvent
		if err := scanOutbox(rows, &e); err != nil {
			rows.Close()
			return res, err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Claimed = len(events)

	publishedIDs := make([]int64, 0, len(events))
	for i := range events {
		if perr := publish(events[i]); perr != nil {
			// Record and move on. The row is rescheduled with exponential backoff,
			// or dead-lettered if it has exhausted its budget.
			dead, merr := markFailed(ctx, tx, events[i].ID, events[i].Attempts, perr)
			if merr != nil {
				return res, merr
			}
			res.Failed++
			if dead {
				res.DeadLettered++
			}
			continue
		}
		publishedIDs = append(publishedIDs, events[i].ID)
	}

	if len(publishedIDs) > 0 {
		if _, err = tx.Exec(ctx,
			`UPDATE outbox_events SET published_at = now() WHERE id = ANY($1)`, publishedIDs,
		); err != nil {
			return res, err
		}
		res.Published = len(publishedIDs)
	}

	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// markFailed records a failed attempt and schedules the retry. Reports whether the
// row was dead-lettered.
//
// Backoff and the give-up decision are done in ONE statement so they cannot
// disagree: attempts, the next gate, and dead_at are all derived from the same
// incremented value. Doing it in two statements would leave a window where a row is
// over budget but still schedulable.
//
// Growth is exponential (2^attempts seconds) and capped. Exponential matters
// because the common cause of failure is the broker being down: retrying a dead
// Kafka every second from every relay worker adds load to a system already in
// trouble, and backing off is how the retry storm is avoided.
func markFailed(ctx context.Context, tx pgx.Tx, id int64, attempts int, cause error) (bool, error) {
	next := attempts + 1
	dead := next >= MaxPublishAttempts

	// Backoff is computed in Go rather than in SQL. Doing POWER(2, $n) inline forced
	// the same parameter to be an int in one place and a float in another, which
	// Postgres refuses to type-infer ("inconsistent types deduced for parameter").
	// Computing it here is also plainly readable and unit-testable.
	_, err := tx.Exec(ctx,
		`UPDATE outbox_events
		    SET attempts        = $2,
		        last_error      = $3,
		        next_attempt_at = now() + make_interval(secs => $4),
		        dead_at         = CASE WHEN $5 THEN now() ELSE NULL END
		  WHERE id = $1`,
		id, next, truncateErr(cause), backoffSeconds(next), dead,
	)
	return dead, err
}

// backoffSeconds grows exponentially (2, 4, 8, 16 ...) and is capped.
//
// Exponential matters because the usual cause of failure is the broker being down:
// every relay worker retrying every second adds load to a system already in
// trouble. Backing off is how a retry storm is avoided. The cap stops the interval
// growing so large that a recovered event is never noticed again.
func backoffSeconds(attempt int) int {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 30 { // guard the shift; the cap applies well before this anyway
		return maxBackoffSeconds
	}
	if b := 1 << attempt; b < maxBackoffSeconds {
		return b
	}
	return maxBackoffSeconds
}

// truncateErr bounds what we store: a driver error can be enormous, and last_error
// is for a human triaging, not for a full stack trace.
func truncateErr(err error) string {
	const max = 500
	s := err.Error()
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ListDead returns dead-lettered events for operator triage — the queue of things
// that need a human. An empty result here is what "healthy" looks like.
func (r *OutboxRepository) ListDead(ctx context.Context, limit int) ([]OutboxEvent, error) {
	const q = `SELECT ` + outboxColumns + ` FROM outbox_events
	           WHERE dead_at IS NOT NULL ORDER BY id DESC LIMIT $1`
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

// PendingCount is the outbox LAG: how many events are waiting to be published.
//
// This is the single most important number to alarm on. A steadily climbing lag
// means the relay is down or Kafka is unreachable, and every consumer downstream is
// silently working from stale data. It is wired into metrics in F6.
func (r *OutboxRepository) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL`,
	).Scan(&n)
	return n, err
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
