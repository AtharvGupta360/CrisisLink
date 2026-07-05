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
		UPDATE units SET status = $1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + unitColumns
	var u models.Unit
	if err := scanUnit(r.pool.QueryRow(ctx, q, status, id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}
