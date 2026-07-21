package dispatch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

// Repository-level outcomes of a reservation. The service translates these into
// its own sentinels; the repo can't import the service (that would be a cycle),
// so it exports its own. Distinct values (not raw pgx.ErrNoRows) so the caller can
// tell "no such unit" from "no such incident".
var (
	ErrUnitNotFound            = errors.New("unit not found")
	ErrUnitNotAvailable        = errors.New("unit not available")
	ErrIncidentNotFound        = errors.New("incident not found")
	ErrIncidentNotDispatchable = errors.New("incident not dispatchable")
	// ErrReservationConflict means the optimistic path kept losing the version
	// compare-and-swap to concurrent (benign) writers until retries ran out. The
	// caller can just try again.
	ErrReservationConflict = errors.New("reservation conflict, retries exhausted")

	// P15 lifecycle outcomes.
	ErrDispatchNotFound          = errors.New("dispatch not found")
	ErrIllegalDispatchTransition = errors.New("illegal dispatch transition")
)

// dispatchColumns is the standard projection; order must match scanDispatch.
const dispatchColumns = `id::text, incident_id::text, unit_id::text, status, created_at, updated_at`

func scanDispatch(s dbx.Scanner, d *Dispatch) error {
	return s.Scan(&d.ID, &d.IncidentID, &d.UnitID, &d.Status, &d.CreatedAt, &d.UpdatedAt)
}

// maxOptimisticRetries caps how many times the optimistic path re-reads and
// retries the compare-and-swap before giving up with ErrReservationConflict.
const maxOptimisticRetries = 3

// EventWriter is the outbox SEAM. This module must be able to RECORD that
// something happened, atomically with the state change that caused it — but it has
// no business knowing the outbox table's shape. Declared here (by the consumer),
// satisfied by *outbox.OutboxRepository.
//
// The pgx.Tx argument is the whole design: the event and the domain write land in
// ONE commit, which is what defeats the dual-write problem. Split this module into
// its own service later and this interface is the seam that becomes a real
// event-bus client.
type EventWriter interface {
	WriteTx(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, payload any) error
}

// UnitReserver and IncidentTracker are the SEAMS the dispatch module depends on.
// Declared here, by the consumer, and satisfied by *unit.UnitRepository and
// *incident.IncidentRepository — dependency inversion, so dispatch names the
// capabilities it needs rather than importing another module's tables.
//
// Every method carries the caller's pgx.Tx. That is the deliberate design: the
// reservation still commits as ONE transaction across four modules' tables, so the
// no-double-booking guarantee is exactly as strong as before the split. What
// changed is ownership — dispatch can no longer write units or incidents behind
// their modules' backs.
//
// This is also what "microservices-ready" concretely means here: extract unit into
// its own service and only UnitReserver's implementation changes (an RPC or an
// event, plus a saga to replace the shared transaction). No dispatch logic moves.
type UnitReserver interface {
	LockForReserveTx(ctx context.Context, tx pgx.Tx, unitID string) (string, error)
	ReadVersionTx(ctx context.Context, tx pgx.Tx, unitID string) (string, int, error)
	MarkReservedTx(ctx context.Context, tx pgx.Tx, unitID string) error
	MarkReservedCASTx(ctx context.Context, tx pgx.Tx, unitID string, version int) (bool, error)
	SetStatusTx(ctx context.Context, tx pgx.Tx, unitID, status string) error
}

type IncidentTracker interface {
	StatusTx(ctx context.Context, tx pgx.Tx, id string) (string, error)
	MarkDispatchedTx(ctx context.Context, tx pgx.Tx, id string) error
	ResolveIfDispatchedTx(ctx context.Context, tx pgx.Tx, id string) error
	RevertToVerifiedTx(ctx context.Context, tx pgx.Tx, id string) error
}

type DispatchRepository struct {
	pool      *pgxpool.Pool
	units     UnitReserver
	incidents IncidentTracker
	events    EventWriter
}

func NewDispatchRepository(pool *pgxpool.Pool, units UnitReserver, incidents IncidentTracker, events EventWriter) *DispatchRepository {
	return &DispatchRepository{pool: pool, units: units, incidents: incidents, events: events}
}

// Reserve atomically assigns a unit to an incident, guaranteeing no double-booking.
// This is the concurrency core. The whole thing is ONE transaction:
//
//	BEGIN
//	  SELECT unit FOR UPDATE   -- lock the row; concurrent reservers block here
//	  re-check status          -- still 'available'? if not, we lost the race
//	  validate incident
//	  UPDATE unit -> reserved
//	  INSERT dispatch          -- partial unique index is the structural backstop
//	  UPDATE incident -> dispatched
//	COMMIT                     -- releases the lock; a blocked reserver now wakes,
//	                              re-reads 'reserved', and backs off
func (r *DispatchRepository) Reserve(ctx context.Context, incidentID, unitID string) (*Dispatch, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Safe no-op if we already committed. If any step below returns early, this
	// rolls the whole thing back — the unit is never left half-reserved.
	defer tx.Rollback(ctx)

	// 1. LOCK the unit row. FOR UPDATE takes a row-level exclusive lock: any other
	//    transaction that also does SELECT ... FOR UPDATE on this same row now
	//    WAITS here until we commit or roll back. This is what closes the
	//    check-then-act gap — the read and the pending write are one atomic unit.
	unitStatus, err := r.units.LockForReserveTx(ctx, tx, unitID)
	if err != nil {
		return nil, err
	}

	// 2. RE-CHECK under the lock. We now hold the row; nobody can change it. If it
	//    isn't available anymore, a concurrent dispatcher beat us — bail out.
	if unitStatus != unit.UnitAvailable {
		return nil, ErrUnitNotAvailable
	}

	// 3. Validate the incident exists and is still in a dispatchable state.
	incStatus, err := r.incidents.StatusTx(ctx, tx, incidentID)
	if err != nil {
		return nil, err
	}
	if incStatus == incident.StatusResolved || incStatus == incident.StatusCancelled {
		return nil, ErrIncidentNotDispatchable
	}

	// 4. Flip the unit to reserved. Bump version too so it stays monotonic across
	//    every unit mutation (the optimistic path in P14 relies on that).
	if err = r.units.MarkReservedTx(ctx, tx, unitID); err != nil {
		return nil, err
	}

	// 5. Record the assignment. The partial unique index (one active dispatch per
	//    unit) is the structural safety net: a 23505 here means someone raced us
	//    past the lock logic — treat it exactly like "unit not available".
	var d Dispatch
	err = tx.QueryRow(ctx,
		`INSERT INTO dispatches (incident_id, unit_id)
		 VALUES ($1::uuid, $2::uuid)
		 RETURNING id::text, incident_id::text, unit_id::text, status, created_at, updated_at`,
		incidentID, unitID,
	).Scan(&d.ID, &d.IncidentID, &d.UnitID, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUnitNotAvailable
		}
		return nil, err
	}

	// 6. Promote the incident to dispatched. Guarded to reported/verified so it's
	//    idempotent if another unit already moved it (an incident can hold many
	//    dispatches).
	if err = r.incidents.MarkDispatchedTx(ctx, tx, incidentID); err != nil {
		return nil, err
	}

	// 7. Emit the domain event INTO THE SAME TRANSACTION (transactional outbox).
	//    The event commits atomically with the reservation — no dual-write gap.
	if err = r.events.WriteTx(ctx, tx, outbox.AggregateDispatch, d.ID, outbox.EventDispatchCreated, map[string]any{
		"dispatchId": d.ID,
		"incidentId": d.IncidentID,
		"unitId":     d.UnitID,
	}); err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}

// ReserveOptimistic is the P14 optimistic counterpart to Reserve. Instead of
// locking the unit row up front, it reads (status, version) WITHOUT a lock, then
// wins the unit with a compare-and-swap UPDATE guarded by "version = the value I
// read". If a concurrent writer moved version in between, the CAS matches zero
// rows and we retry from a fresh read. No lock is held during the read/decide gap.
//
// It loops maxOptimisticRetries times. A lost CAS where the unit is *still*
// available means a benign concurrent write (e.g. an admin edit) — worth retrying.
// A re-read that shows the unit no longer available means a rival reserved it — a
// terminal ErrUnitNotAvailable, no point retrying.
func (r *DispatchRepository) ReserveOptimistic(ctx context.Context, incidentID, unitID string) (*Dispatch, error) {
	for attempt := 0; attempt < maxOptimisticRetries; attempt++ {
		d, retry, err := r.tryReserveOptimistic(ctx, incidentID, unitID)
		if err != nil {
			return nil, err
		}
		if !retry {
			return d, nil
		}
		// retry: version moved but the unit still looked available — loop.
	}
	return nil, ErrReservationConflict
}

// tryReserveOptimistic runs one optimistic attempt. It returns (dispatch, false,
// nil) on success, (nil, true, nil) when the CAS lost but a retry is worthwhile,
// or (nil, false, err) on a terminal outcome.
func (r *DispatchRepository) tryReserveOptimistic(ctx context.Context, incidentID, unitID string) (*Dispatch, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	// 1. Read status + version WITHOUT locking. This is the optimistic bet: assume
	//    nobody else is racing, so don't pay for a lock.
	status, version, err := r.units.ReadVersionTx(ctx, tx, unitID)
	if err != nil {
		return nil, false, err
	}
	if status != unit.UnitAvailable {
		return nil, false, ErrUnitNotAvailable
	}

	// 2. Validate the incident (same rules as the pessimistic path).
	incStatus, err := r.incidents.StatusTx(ctx, tx, incidentID)
	if err != nil {
		return nil, false, err
	}
	if incStatus == incident.StatusResolved || incStatus == incident.StatusCancelled {
		return nil, false, ErrIncidentNotDispatchable
	}

	// 3. Compare-and-swap: succeed ONLY if the row is untouched since our read
	//    (version unchanged). This is the whole optimistic mechanism.
	won, err := r.units.MarkReservedCASTx(ctx, tx, unitID, version)
	if err != nil {
		return nil, false, err
	}
	if !won {
		// Someone changed the row between our read and this write. Signal a retry;
		// the next attempt's fresh read decides success vs terminal-unavailable.
		return nil, true, nil
	}

	// 4. We won the unit. Record the dispatch (partial unique index is still the
	//    structural backstop; 23505 -> treat as taken).
	var d Dispatch
	err = tx.QueryRow(ctx,
		`INSERT INTO dispatches (incident_id, unit_id)
		 VALUES ($1::uuid, $2::uuid)
		 RETURNING id::text, incident_id::text, unit_id::text, status, created_at, updated_at`,
		incidentID, unitID,
	).Scan(&d.ID, &d.IncidentID, &d.UnitID, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, false, ErrUnitNotAvailable
		}
		return nil, false, err
	}

	// 5. Promote the incident (idempotent).
	if err = r.incidents.MarkDispatchedTx(ctx, tx, incidentID); err != nil {
		return nil, false, err
	}

	// 6. Emit the domain event in the same transaction (transactional outbox).
	if err = r.events.WriteTx(ctx, tx, outbox.AggregateDispatch, d.ID, outbox.EventDispatchCreated, map[string]any{
		"dispatchId": d.ID,
		"incidentId": d.IncidentID,
		"unitId":     d.UnitID,
	}); err != nil {
		return nil, false, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &d, false, nil
}

// GetByID returns a single dispatch.
func (r *DispatchRepository) GetByID(ctx context.Context, id string) (*Dispatch, error) {
	const q = `SELECT ` + dispatchColumns + ` FROM dispatches WHERE id = $1::uuid`
	var d Dispatch
	if err := scanDispatch(r.pool.QueryRow(ctx, q, id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListByIncident returns an incident's dispatches, newest first.
func (r *DispatchRepository) ListByIncident(ctx context.Context, incidentID string) ([]Dispatch, error) {
	const q = `SELECT ` + dispatchColumns + ` FROM dispatches WHERE incident_id = $1::uuid ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Dispatch, 0)
	for rows.Next() {
		var d Dispatch
		if err := scanDispatch(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AdvanceStatus moves a dispatch along its lifecycle and keeps everything in sync,
// atomically. It applies the P13 locking lesson to a new row: the dispatch itself
// is a check-then-act target (two admins could patch it at once), so we lock it
// FOR UPDATE, re-read, validate the transition, then in the same transaction:
//   - update the dispatch status,
//   - sync the UNIT status (mirror the active phase, or free it to available),
//   - if the dispatch just completed and no active dispatches remain for the
//     incident, resolve the incident.
func (r *DispatchRepository) AdvanceStatus(ctx context.Context, dispatchID, newStatus string) (*Dispatch, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Lock the dispatch row and read its current state.
	var cur Dispatch
	err = scanDispatch(
		tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM dispatches WHERE id = $1::uuid FOR UPDATE`, dispatchID),
		&cur,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDispatchNotFound
	}
	if err != nil {
		return nil, err
	}

	// Validate the transition under the lock.
	if !CanTransitionDispatch(cur.Status, newStatus) {
		return nil, ErrIllegalDispatchTransition
	}

	// Update the dispatch.
	var d Dispatch
	err = scanDispatch(
		tx.QueryRow(ctx,
			`UPDATE dispatches SET status = $1, updated_at = now() WHERE id = $2::uuid RETURNING `+dispatchColumns,
			newStatus, dispatchID,
		),
		&d,
	)
	if err != nil {
		return nil, err
	}

	// Sync the unit: active phase mirrors onto the unit; terminal frees it.
	if err = r.units.SetStatusTx(ctx, tx, cur.UnitID, UnitStatusForDispatch(newStatus)); err != nil {
		return nil, err
	}

	// If this completed the last active dispatch, resolve the incident.
	if newStatus == DispatchCompleted {
		var active int
		if err = tx.QueryRow(ctx,
			`SELECT count(*) FROM dispatches WHERE incident_id = $1::uuid AND status IN ('reserved','en_route','on_scene')`,
			cur.IncidentID,
		).Scan(&active); err != nil {
			return nil, err
		}
		if active == 0 {
			if err = r.incidents.ResolveIfDispatchedTx(ctx, tx, cur.IncidentID); err != nil {
				return nil, err
			}
		}

		// Emit the completion event in the SAME transaction (outbox, P19/P22).
		if err = r.events.WriteTx(ctx, tx, outbox.AggregateDispatch, d.ID, outbox.EventDispatchCompleted, map[string]any{
			"dispatchId":       d.ID,
			"incidentId":       d.IncidentID,
			"unitId":           d.UnitID,
			"incidentResolved": active == 0,
		}); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}
