package unit

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
)

// ErrUnitNotAvailable means the unit exists but is not in the available state.
var ErrUnitNotAvailable = errors.New("unit not available")

// --- Reservation seams -------------------------------------------------------
//
// These four methods are the SEAM the dispatch module reserves units through.
// Dispatch used to run this SQL itself, which meant the units table had two
// owners: any change to the status vocabulary or the version column could break
// dispatch silently. Now dispatch states its INTENT ("lock this unit", "reserve it
// if nothing changed") and the unit module decides how that is expressed in SQL.
//
// Every method takes the caller's pgx.Tx, so a reservation is still ONE atomic
// transaction spanning both modules. That is the whole "shared-tx interface"
// pattern: real boundaries, without giving up the transaction that makes
// double-booking impossible.

// LockForReserveTx takes a row-level exclusive lock on the unit and returns its
// status. This is the PESSIMISTIC path: any concurrent transaction that also does
// SELECT ... FOR UPDATE on this row WAITS here until we commit or roll back, which
// is what closes the check-then-act gap. The caller must re-check the returned
// status under that lock before acting on it.
func (r *UnitRepository) LockForReserveTx(ctx context.Context, tx pgx.Tx, unitID string) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM units WHERE id = $1::uuid FOR UPDATE`, unitID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnitNotFound
	}
	return status, err
}

// ReadVersionTx reads status + version WITHOUT locking — the OPTIMISTIC path's
// initial bet that nobody else is racing us, so we don't pay for a lock.
func (r *UnitRepository) ReadVersionTx(ctx context.Context, tx pgx.Tx, unitID string) (string, int, error) {
	var status string
	var version int
	err := tx.QueryRow(ctx, `SELECT status, version FROM units WHERE id = $1::uuid`, unitID).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrUnitNotFound
	}
	return status, version, err
}

// MarkReservedTx flips the unit to reserved. version is bumped so it stays
// monotonic across EVERY unit mutation — the optimistic path's CAS depends on that
// being true even for writes that didn't go through the CAS.
func (r *UnitRepository) MarkReservedTx(ctx context.Context, tx pgx.Tx, unitID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE units SET status = 'reserved', version = version + 1, updated_at = now() WHERE id = $1::uuid`,
		unitID)
	return err
}

// MarkReservedCASTx is the compare-and-swap: it reserves the unit ONLY if the row
// is untouched since the caller read `version`. Reports whether it won. A false
// return is not an error — it means a concurrent writer got there first and the
// caller should retry with a fresh read.
func (r *UnitRepository) MarkReservedCASTx(ctx context.Context, tx pgx.Tx, unitID string, version int) (bool, error) {
	ct, err := tx.Exec(ctx,
		`UPDATE units SET status = 'reserved', version = version + 1, updated_at = now()
		 WHERE id = $1::uuid AND version = $2`,
		unitID, version)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// SetStatusTx moves a unit to an arbitrary status, used as a dispatch advances
// through its lifecycle (and to free the unit when it terminates).
func (r *UnitRepository) SetStatusTx(ctx context.Context, tx pgx.Tx, unitID, status string) error {
	_, err := tx.Exec(ctx,
		`UPDATE units SET status = $1, version = version + 1, updated_at = now() WHERE id = $2::uuid`,
		status, unitID)
	return err
}

type UnitRepository struct {
	pool *pgxpool.Pool
}

func NewUnitRepository(pool *pgxpool.Pool) *UnitRepository {
	return &UnitRepository{pool: pool}
}

// unitColumns projection; ST_X=longitude, ST_Y=latitude. Order must match scanUnit.
const unitColumns = `id::text, call_sign, type, status,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanUnit(s dbx.Scanner, u *Unit) error {
	return s.Scan(
		&u.ID, &u.CallSign, &u.Type, &u.Status,
		&u.Longitude, &u.Latitude, &u.CreatedAt, &u.UpdatedAt,
	)
}

// Create inserts a unit (status defaults to 'available'). $3=lng, $4=lat.
func (r *UnitRepository) Create(ctx context.Context, u *Unit) error {
	const q = `
		INSERT INTO units (call_sign, type, location)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		RETURNING id::text, status, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		u.CallSign, u.Type, u.Longitude, u.Latitude,
	).Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
}

func (r *UnitRepository) GetByID(ctx context.Context, id string) (*Unit, error) {
	const q = `SELECT ` + unitColumns + ` FROM units WHERE id = $1::uuid`
	var u Unit
	if err := scanUnit(r.pool.QueryRow(ctx, q, id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// List returns units, optionally filtered by status and/or type (empty string =
// no filter for that field), ordered by call_sign, paginated.
func (r *UnitRepository) List(ctx context.Context, status, unitType string, limit, offset int) ([]Unit, error) {
	const q = `
		SELECT ` + unitColumns + ` FROM units
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR type = $2)
		ORDER BY call_sign
		LIMIT $3 OFFSET $4`
	rows, err := r.pool.Query(ctx, q, status, unitType, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Unit, 0, limit)
	for rows.Next() {
		var u Unit
		if err := scanUnit(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateStatus sets a new status (P10: admin override; disciplined lifecycle
// transitions come in P15) and returns the updated unit.
func (r *UnitRepository) UpdateStatus(ctx context.Context, id, status string) (*Unit, error) {
	const q = `
		UPDATE units SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + unitColumns
	var u Unit
	if err := scanUnit(r.pool.QueryRow(ctx, q, status, id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// FindNearestAvailable returns the nearest AVAILABLE units to (lat,lng), nearest
// first, each with DistanceMeters. Optionally filters by unitType ("" = any).
//
// This is the KNN candidate query (P11). The `<->` KNN operator lets the GiST
// index (the partial idx_units_available_gix) return rows already in nearest-first
// order and stop at LIMIT — an index scan, O(k log n) — instead of computing
// ST_Distance for every row and sorting. ST_Distance in the SELECT is only for
// the display distance, not the ordering. Params: $1=lng, $2=lat, $3=type, $4=limit.
func (r *UnitRepository) FindNearestAvailable(ctx context.Context, lat, lng float64, unitType string, limit int) ([]Unit, error) {
	const q = `
		SELECT ` + unitColumns + `,
		       ST_Distance(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM units
		WHERE status = 'available'
		  AND ($3 = '' OR type = $3)
		ORDER BY location::geography <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
		LIMIT $4`
	rows, err := r.pool.Query(ctx, q, lng, lat, unitType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Unit, 0, limit)
	for rows.Next() {
		var u Unit
		if err := rows.Scan(
			&u.ID, &u.CallSign, &u.Type, &u.Status,
			&u.Longitude, &u.Latitude, &u.CreatedAt, &u.UpdatedAt, &u.DistanceMeters,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
