package transport

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
)

// Booking outcomes. Each names a distinct reason a seat claim was refused, so the
// handler can map them to precise HTTP codes instead of a generic conflict.
var (
	ErrTransportNotFound    = errors.New("transport not found")
	ErrTransportUnavailable = errors.New("transport is not available")
	ErrInsufficientSeats    = errors.New("not enough free seats")
	ErrBookingNotFound      = errors.New("booking not found")
	ErrBookingNotActive     = errors.New("booking is not active")
	ErrIncidentNotFound     = errors.New("incident not found")
)

// EventWriter is the outbox SEAM (see dispatch/victim for the same pattern): this
// module must record that seats were booked, atomically with booking them, without
// knowing anything about the outbox table.
type EventWriter interface {
	WriteTx(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, payload any) error
}

type TransportRepository struct {
	pool   *pgxpool.Pool
	events EventWriter
}

func NewTransportRepository(pool *pgxpool.Pool, events EventWriter) *TransportRepository {
	return &TransportRepository{pool: pool, events: events}
}

const transportColumns = `id::text, call_sign, kind, capacity, seats_taken, status,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, version, created_at, updated_at`

func scanTransport(s dbx.Scanner, t *Transport) error {
	if err := s.Scan(
		&t.ID, &t.CallSign, &t.Kind, &t.Capacity, &t.SeatsTaken, &t.Status,
		&t.Longitude, &t.Latitude, &t.Version, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return err
	}
	t.SeatsFree = t.Capacity - t.SeatsTaken // derived, never stored
	return nil
}

// Create registers a vehicle (seats_taken starts 0, status 'available').
func (r *TransportRepository) Create(ctx context.Context, t *Transport) error {
	const q = `
		INSERT INTO transports (call_sign, kind, capacity, location)
		VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint($4, $5), 4326))
		RETURNING id::text, seats_taken, status, version, created_at, updated_at`
	if err := r.pool.QueryRow(ctx, q,
		t.CallSign, t.Kind, t.Capacity, t.Longitude, t.Latitude,
	).Scan(&t.ID, &t.SeatsTaken, &t.Status, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	t.SeatsFree = t.Capacity - t.SeatsTaken
	return nil
}

func (r *TransportRepository) GetByID(ctx context.Context, id string) (*Transport, error) {
	const q = `SELECT ` + transportColumns + ` FROM transports WHERE id = $1::uuid`
	var t Transport
	if err := scanTransport(r.pool.QueryRow(ctx, q, id), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List returns transports, optionally filtered by status and kind.
func (r *TransportRepository) List(ctx context.Context, status, kind string, limit, offset int) ([]Transport, error) {
	const q = `
		SELECT ` + transportColumns + ` FROM transports
		WHERE ($1 = '' OR status = $1) AND ($2 = '' OR kind = $2)
		ORDER BY call_sign
		LIMIT $3 OFFSET $4`
	rows, err := r.pool.Query(ctx, q, status, kind, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Transport, 0, limit)
	for rows.Next() {
		var t Transport
		if err := scanTransport(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindNearestAvailable returns the nearest vehicles that are available AND have at
// least `seats` free, nearest first (KNN via the <-> operator on the partial GiST
// index). Note the candidacy test is a QUANTITY comparison — "can it fit this
// group" — not merely "is it free", which is what distinguishes this from the unit
// and shelter searches. Params: $1=lng, $2=lat, $3=seats, $4=limit.
func (r *TransportRepository) FindNearestAvailable(ctx context.Context, lat, lng float64, seats, limit int) ([]Transport, error) {
	const q = `
		SELECT ` + transportColumns + `,
		       ST_Distance(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM transports
		WHERE status = 'available' AND seats_taken + $3 <= capacity
		ORDER BY location::geography <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
		LIMIT $4`
	rows, err := r.pool.Query(ctx, q, lng, lat, seats, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Transport, 0, limit)
	for rows.Next() {
		var t Transport
		if err := rows.Scan(
			&t.ID, &t.CallSign, &t.Kind, &t.Capacity, &t.SeatsTaken, &t.Status,
			&t.Longitude, &t.Latitude, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DistanceMeters,
		); err != nil {
			return nil, err
		}
		t.SeatsFree = t.Capacity - t.SeatsTaken
		out = append(out, t)
	}
	return out, rows.Err()
}

// BookSeats claims N seats on a transport for an incident, atomically.
//
// THE INVARIANT: seats_taken must never exceed capacity, no matter how many
// dispatchers book the same vehicle in the same millisecond.
//
// The guard is ONE conditional UPDATE:
//
//	UPDATE ... SET seats_taken = seats_taken + $n
//	 WHERE status = 'available' AND seats_taken + $n <= capacity
//
// Test and increment happen inside a single statement, so Postgres serialises
// concurrent updates to that row and the loser's WHERE simply evaluates false. No
// explicit FOR UPDATE is needed — that is only required when the check and the
// write are separate statements (the boolean unit case).
//
// ALL-OR-NOTHING is a domain decision with a technical consequence: a group of five
// is never split across vehicles, so there must be no intermediate state where
// three seats were taken and the rest failed. A single statement guarantees that;
// a read-then-write loop would not.
func (r *TransportRepository) BookSeats(ctx context.Context, transportID, incidentID string, seats int) (*Booking, *Transport, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// 1. Validate the incident exists. Done first so a bad incident id is reported
	//    as such rather than surfacing later as a foreign-key violation.
	var incExists bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM incidents WHERE id = $1::uuid)`, incidentID,
	).Scan(&incExists); err != nil {
		return nil, nil, err
	}
	if !incExists {
		return nil, nil, ErrIncidentNotFound
	}

	// 2. The guarded quantity increment — the whole invariant, in one statement.
	ct, err := tx.Exec(ctx,
		`UPDATE transports
		    SET seats_taken = seats_taken + $2, version = version + 1, updated_at = now()
		  WHERE id = $1::uuid AND status = 'available' AND seats_taken + $2 <= capacity`,
		transportID, seats,
	)
	if err != nil {
		return nil, nil, err
	}
	if ct.RowsAffected() == 0 {
		// Zero rows means the guard refused us, but not why. Re-read inside the
		// transaction to return a precise reason.
		var status string
		var taken, capacity int
		e := tx.QueryRow(ctx,
			`SELECT status, seats_taken, capacity FROM transports WHERE id = $1::uuid`, transportID,
		).Scan(&status, &taken, &capacity)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, nil, ErrTransportNotFound
		}
		if e != nil {
			return nil, nil, e
		}
		if status != StatusAvailable {
			metrics.ReservationConflicts.WithLabelValues("transport", "unavailable").Inc()
			return nil, nil, ErrTransportUnavailable
		}
		metrics.ReservationConflicts.WithLabelValues("transport", "no_seats").Inc()
		return nil, nil, ErrInsufficientSeats
	}

	// 3. Record the claim.
	var b Booking
	if err = tx.QueryRow(ctx,
		`INSERT INTO transport_bookings (transport_id, incident_id, seats)
		 VALUES ($1::uuid, $2::uuid, $3)
		 RETURNING id::text, transport_id::text, incident_id::text, seats, status, created_at, updated_at`,
		transportID, incidentID, seats,
	).Scan(&b.ID, &b.TransportID, &b.IncidentID, &b.Seats, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, nil, err
	}

	// 4. Read back the updated vehicle so the caller sees fresh seat counts.
	var t Transport
	if err = scanTransport(
		tx.QueryRow(ctx, `SELECT `+transportColumns+` FROM transports WHERE id = $1::uuid`, transportID), &t,
	); err != nil {
		return nil, nil, err
	}

	// 5. Emit the domain event in the SAME transaction (transactional outbox).
	if err = r.events.WriteTx(ctx, tx, outbox.AggregateTransport, b.ID, outbox.EventTransportBooked, map[string]any{
		"bookingId":   b.ID,
		"transportId": b.TransportID,
		"incidentId":  b.IncidentID,
		"seats":       b.Seats,
		"seatsTaken":  t.SeatsTaken,
	}); err != nil {
		return nil, nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &b, &t, nil
}

// CancelBooking releases a booking's seats back to the vehicle.
//
// This is the inverse guard, and it has its own race: two concurrent cancellations
// of the SAME booking would each try to give the seats back, double-refunding and
// corrupting the count downward. The booking row is therefore flipped with a
// guarded UPDATE on its status, and only the transaction that actually moved it out
// of 'booked' is allowed to release the seats. RowsAffected is the arbiter, exactly
// as it is on the way in.
func (r *TransportRepository) CancelBooking(ctx context.Context, bookingID string) (*Booking, *Transport, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// Claim the cancellation. Guarded on status='booked' so only ONE concurrent
	// caller can win, which is what makes the seat refund below safe to run once.
	var b Booking
	err = tx.QueryRow(ctx,
		`UPDATE transport_bookings SET status = 'cancelled', updated_at = now()
		  WHERE id = $1::uuid AND status = 'booked'
		  RETURNING id::text, transport_id::text, incident_id::text, seats, status, created_at, updated_at`,
		bookingID,
	).Scan(&b.ID, &b.TransportID, &b.IncidentID, &b.Seats, &b.Status, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either it does not exist, or someone else already moved it. Distinguish.
		var exists bool
		if e := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM transport_bookings WHERE id = $1::uuid)`, bookingID,
		).Scan(&exists); e != nil {
			return nil, nil, e
		}
		if !exists {
			return nil, nil, ErrBookingNotFound
		}
		return nil, nil, ErrBookingNotActive
	}
	if err != nil {
		return nil, nil, err
	}

	// Refund the seats. GREATEST(...) is a floor guard: the CHECK constraint already
	// forbids a negative count, but clamping means a hypothetical accounting bug
	// degrades to a wrong number rather than a failed transaction mid-evacuation.
	if _, err = tx.Exec(ctx,
		`UPDATE transports
		    SET seats_taken = GREATEST(seats_taken - $2, 0), version = version + 1, updated_at = now()
		  WHERE id = $1::uuid`,
		b.TransportID, b.Seats,
	); err != nil {
		return nil, nil, err
	}

	var t Transport
	if err = scanTransport(
		tx.QueryRow(ctx, `SELECT `+transportColumns+` FROM transports WHERE id = $1::uuid`, b.TransportID), &t,
	); err != nil {
		return nil, nil, err
	}

	if err = r.events.WriteTx(ctx, tx, outbox.AggregateTransport, b.ID, outbox.EventTransportReleased, map[string]any{
		"bookingId":   b.ID,
		"transportId": b.TransportID,
		"seats":       b.Seats,
		"seatsTaken":  t.SeatsTaken,
	}); err != nil {
		return nil, nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &b, &t, nil
}

// ListBookings returns an incident's transport bookings, newest first.
func (r *TransportRepository) ListBookings(ctx context.Context, incidentID string, limit int) ([]Booking, error) {
	const q = `
		SELECT id::text, transport_id::text, incident_id::text, seats, status, created_at, updated_at
		FROM transport_bookings WHERE incident_id = $1::uuid
		ORDER BY created_at DESC LIMIT $2`
	rows, err := r.pool.Query(ctx, q, incidentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Booking, 0, limit)
	for rows.Next() {
		var b Booking
		if err := rows.Scan(&b.ID, &b.TransportID, &b.IncidentID, &b.Seats, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
