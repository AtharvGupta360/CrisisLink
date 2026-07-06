package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

type UnitRepository struct {
	pool *pgxpool.Pool
}

func NewUnitRepository(pool *pgxpool.Pool) *UnitRepository {
	return &UnitRepository{pool: pool}
}

// unitColumns projection; ST_X=longitude, ST_Y=latitude. Order must match scanUnit.
const unitColumns = `id::text, call_sign, type, status,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanUnit(s scanner, u *models.Unit) error {
	return s.Scan(
		&u.ID, &u.CallSign, &u.Type, &u.Status,
		&u.Longitude, &u.Latitude, &u.CreatedAt, &u.UpdatedAt,
	)
}

// Create inserts a unit (status defaults to 'available'). $3=lng, $4=lat.
func (r *UnitRepository) Create(ctx context.Context, u *models.Unit) error {
	const q = `
		INSERT INTO units (call_sign, type, location)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		RETURNING id::text, status, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		u.CallSign, u.Type, u.Longitude, u.Latitude,
	).Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
}

func (r *UnitRepository) GetByID(ctx context.Context, id string) (*models.Unit, error) {
	const q = `SELECT ` + unitColumns + ` FROM units WHERE id = $1::uuid`
	var u models.Unit
	if err := scanUnit(r.pool.QueryRow(ctx, q, id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// List returns units, optionally filtered by status and/or type (empty string =
// no filter for that field), ordered by call_sign, paginated.
func (r *UnitRepository) List(ctx context.Context, status, unitType string, limit, offset int) ([]models.Unit, error) {
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

	out := make([]models.Unit, 0, limit)
	for rows.Next() {
		var u models.Unit
		if err := scanUnit(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateStatus sets a new status (P10: admin override; disciplined lifecycle
// transitions come in P15) and returns the updated unit.
func (r *UnitRepository) UpdateStatus(ctx context.Context, id, status string) (*models.Unit, error) {
	const q = `
		UPDATE units SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + unitColumns
	var u models.Unit
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
func (r *UnitRepository) FindNearestAvailable(ctx context.Context, lat, lng float64, unitType string, limit int) ([]models.Unit, error) {
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

	out := make([]models.Unit, 0, limit)
	for rows.Next() {
		var u models.Unit
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
