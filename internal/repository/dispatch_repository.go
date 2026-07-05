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
)

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

	// 4. Flip the unit to reserved.
	if _, err = tx.Exec(ctx,
		`UPDATE units SET status = 'reserved', updated_at = now() WHERE id = $1::uuid`,
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
