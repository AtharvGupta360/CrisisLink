package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
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
)

// maxOptimisticRetries caps how many times the optimistic path re-reads and
// retries the compare-and-swap before giving up with ErrReservationConflict.
const maxOptimisticRetries = 3

type DispatchRepository struct {
	pool *pgxpool.Pool
}

func NewDispatchRepository(pool *pgxpool.Pool) *DispatchRepository {
	return &DispatchRepository{pool: pool}
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
func (r *DispatchRepository) Reserve(ctx context.Context, incidentID, unitID string) (*models.Dispatch, error) {
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
	var unitStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM units WHERE id = $1::uuid FOR UPDATE`, unitID).Scan(&unitStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnitNotFound
	}
	if err != nil {
		return nil, err
	}

	// 2. RE-CHECK under the lock. We now hold the row; nobody can change it. If it
	//    isn't available anymore, a concurrent dispatcher beat us — bail out.
	if unitStatus != models.UnitAvailable {
		return nil, ErrUnitNotAvailable
	}

	// 3. Validate the incident exists and is still in a dispatchable state.
	var incStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM incidents WHERE id = $1::uuid`, incidentID).Scan(&incStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIncidentNotFound
	}
	if err != nil {
		return nil, err
	}
	if incStatus == models.StatusResolved || incStatus == models.StatusCancelled {
		return nil, ErrIncidentNotDispatchable
	}

	// 4. Flip the unit to reserved. Bump version too so it stays monotonic across
	//    every unit mutation (the optimistic path in P14 relies on that).
	if _, err = tx.Exec(ctx,
		`UPDATE units SET status = 'reserved', version = version + 1, updated_at = now() WHERE id = $1::uuid`,
		unitID,
	); err != nil {
		return nil, err
	}

	// 5. Record the assignment. The partial unique index (one active dispatch per
	//    unit) is the structural safety net: a 23505 here means someone raced us
	//    past the lock logic — treat it exactly like "unit not available".
	var d models.Dispatch
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
	if _, err = tx.Exec(ctx,
		`UPDATE incidents SET status = 'dispatched', updated_at = now()
		 WHERE id = $1::uuid AND status IN ('reported','verified')`,
		incidentID,
	); err != nil {
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
func (r *DispatchRepository) ReserveOptimistic(ctx context.Context, incidentID, unitID string) (*models.Dispatch, error) {
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
func (r *DispatchRepository) tryReserveOptimistic(ctx context.Context, incidentID, unitID string) (*models.Dispatch, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	// 1. Read status + version WITHOUT locking. This is the optimistic bet: assume
	//    nobody else is racing, so don't pay for a lock.
	var status string
	var version int
	err = tx.QueryRow(ctx, `SELECT status, version FROM units WHERE id = $1::uuid`, unitID).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrUnitNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if status != models.UnitAvailable {
		return nil, false, ErrUnitNotAvailable
	}

	// 2. Validate the incident (same rules as the pessimistic path).
	var incStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM incidents WHERE id = $1::uuid`, incidentID).Scan(&incStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrIncidentNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if incStatus == models.StatusResolved || incStatus == models.StatusCancelled {
		return nil, false, ErrIncidentNotDispatchable
	}

	// 3. Compare-and-swap: succeed ONLY if the row is untouched since our read
	//    (version unchanged). This is the whole optimistic mechanism.
	ct, err := tx.Exec(ctx,
		`UPDATE units SET status = 'reserved', version = version + 1, updated_at = now()
		 WHERE id = $1::uuid AND version = $2`,
		unitID, version,
	)
	if err != nil {
		return nil, false, err
	}
	if ct.RowsAffected() == 0 {
		// Someone changed the row between our read and this write. Signal a retry;
		// the next attempt's fresh read decides success vs terminal-unavailable.
		return nil, true, nil
	}

	// 4. We won the unit. Record the dispatch (partial unique index is still the
	//    structural backstop; 23505 -> treat as taken).
	var d models.Dispatch
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
	if _, err = tx.Exec(ctx,
		`UPDATE incidents SET status = 'dispatched', updated_at = now()
		 WHERE id = $1::uuid AND status IN ('reported','verified')`,
		incidentID,
	); err != nil {
		return nil, false, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &d, false, nil
}
