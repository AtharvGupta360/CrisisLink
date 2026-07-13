package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

type ShelterRepository struct {
	pool *pgxpool.Pool
}

func NewShelterRepository(pool *pgxpool.Pool) *ShelterRepository {
	return &ShelterRepository{pool: pool}
}

// shelterColumns projection; ST_X=longitude, ST_Y=latitude. Order must match
// scanShelter.
const shelterColumns = `id::text, name, capacity, occupancy, status,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanShelter(s scanner, sh *models.Shelter) error {
	if err := s.Scan(
		&sh.ID, &sh.Name, &sh.Capacity, &sh.Occupancy, &sh.Status,
		&sh.Longitude, &sh.Latitude, &sh.CreatedAt, &sh.UpdatedAt,
	); err != nil {
		return err
	}
	sh.AvailableSpots = sh.Capacity - sh.Occupancy // derived, never stored
	return nil
}

// Create inserts a shelter (occupancy defaults to 0, status 'open'). $3=lng, $4=lat.
func (r *ShelterRepository) Create(ctx context.Context, sh *models.Shelter) error {
	const q = `
		INSERT INTO shelters (name, capacity, location)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		RETURNING id::text, occupancy, status, created_at, updated_at`
	if err := r.pool.QueryRow(ctx, q,
		sh.Name, sh.Capacity, sh.Longitude, sh.Latitude,
	).Scan(&sh.ID, &sh.Occupancy, &sh.Status, &sh.CreatedAt, &sh.UpdatedAt); err != nil {
		return err
	}
	sh.AvailableSpots = sh.Capacity - sh.Occupancy
	return nil
}

func (r *ShelterRepository) GetByID(ctx context.Context, id string) (*models.Shelter, error) {
	const q = `SELECT ` + shelterColumns + ` FROM shelters WHERE id = $1::uuid`
	var sh models.Shelter
	if err := scanShelter(r.pool.QueryRow(ctx, q, id), &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}

// List returns shelters, optionally filtered by status (empty = no filter),
// ordered by name, paginated.
func (r *ShelterRepository) List(ctx context.Context, status string, limit, offset int) ([]models.Shelter, error) {
	const q = `
		SELECT ` + shelterColumns + ` FROM shelters
		WHERE ($1 = '' OR status = $1)
		ORDER BY name
		LIMIT $2 OFFSET $3`
	rows, err := r.pool.Query(ctx, q, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Shelter, 0, limit)
	for rows.Next() {
		var sh models.Shelter
		if err := scanShelter(rows, &sh); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// FindNearestOpen returns the nearest shelters to (lat,lng) that are open AND have
// room (occupancy < capacity), nearest first, each with DistanceMeters. This is
// the P17 KNN search — like the units' FindNearestAvailable, but the candidacy
// test is the CAPACITY COUNTER (occupancy < capacity), not a boolean status.
// The partial GiST index (WHERE status='open') serves the `<->` ordering; the
// occupancy < capacity filter is applied on top. Params: $1=lng, $2=lat, $3=limit.
func (r *ShelterRepository) FindNearestOpen(ctx context.Context, lat, lng float64, limit int) ([]models.Shelter, error) {
	const q = `
		SELECT ` + shelterColumns + `,
		       ST_Distance(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM shelters
		WHERE status = 'open' AND occupancy < capacity
		ORDER BY location::geography <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
		LIMIT $3`
	rows, err := r.pool.Query(ctx, q, lng, lat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Shelter, 0, limit)
	for rows.Next() {
		var sh models.Shelter
		if err := rows.Scan(
			&sh.ID, &sh.Name, &sh.Capacity, &sh.Occupancy, &sh.Status,
			&sh.Longitude, &sh.Latitude, &sh.CreatedAt, &sh.UpdatedAt, &sh.DistanceMeters,
		); err != nil {
			return nil, err
		}
		sh.AvailableSpots = sh.Capacity - sh.Occupancy
		out = append(out, sh)
	}
	return out, rows.Err()
}

// UpdateStatus sets a new status (open/closed) and returns the updated shelter.
func (r *ShelterRepository) UpdateStatus(ctx context.Context, id, status string) (*models.Shelter, error) {
	const q = `
		UPDATE shelters SET status = $1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + shelterColumns
	var sh models.Shelter
	if err := scanShelter(r.pool.QueryRow(ctx, q, status, id), &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}
