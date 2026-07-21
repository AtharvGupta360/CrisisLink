package shelter

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
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

func scanShelter(s dbx.Scanner, sh *Shelter) error {
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
func (r *ShelterRepository) Create(ctx context.Context, sh *Shelter) error {
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

func (r *ShelterRepository) GetByID(ctx context.Context, id string) (*Shelter, error) {
	const q = `SELECT ` + shelterColumns + ` FROM shelters WHERE id = $1::uuid`
	var sh Shelter
	if err := scanShelter(r.pool.QueryRow(ctx, q, id), &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}

// GetByIDTx reads a shelter inside the CALLER'S transaction.
//
// This is a module SEAM. The victim module needs the shelter's fresh occupancy
// after admitting someone, but that read has to happen inside the same transaction
// as the admission — otherwise it would either see pre-commit state or require a
// second round trip that could race. Before the modular split, victim code simply
// ran its own `SELECT ... FROM shelters` using shelter's private column list and
// scan helper. That is exactly the coupling module boundaries exist to prevent: a
// change to this table's projection would silently break a different module.
//
// Taking pgx.Tx (not the pool) is what lets modules cooperate in ONE transaction
// while still owning their own tables — the "shared-tx interface" pattern. The day
// shelter becomes its own service with its own database, this is the method whose
// implementation is replaced by a network call or an event, and nothing else moves.
func (r *ShelterRepository) GetByIDTx(ctx context.Context, tx pgx.Tx, id string) (*Shelter, error) {
	const q = `SELECT ` + shelterColumns + ` FROM shelters WHERE id = $1::uuid`
	var sh Shelter
	if err := scanShelter(tx.QueryRow(ctx, q, id), &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}

// Admission outcomes returned by AdmitTx. They live in the shelter module because
// only shelter knows what "closed" or "full" means for its own invariant.
var (
	ErrShelterClosed = errors.New("shelter closed")
	ErrShelterFull   = errors.New("shelter full")
)

// AdmitTx claims ONE bed in a shelter inside the caller's transaction.
//
// This is the no-overflow invariant (P18), and it is the second module SEAM: the
// victim module used to run this UPDATE against the shelters table itself. Now it
// asks shelter to admit someone and shelter decides whether that is legal. The
// invariant and the table that carries it stay in one module.
//
// The guard is a single conditional UPDATE, not a lock + check. `WHERE occupancy <
// capacity` makes the test and the increment ONE atomic statement, so two
// concurrent admissions can never both see room and both take the last bed —
// Postgres serialises the row update and the loser's WHERE simply fails. That is
// why a counter resource needs no explicit FOR UPDATE, unlike a boolean one.
func (r *ShelterRepository) AdmitTx(ctx context.Context, tx pgx.Tx, shelterID string) error {
	ct, err := tx.Exec(ctx,
		`UPDATE shelters SET occupancy = occupancy + 1, updated_at = now()
		 WHERE id = $1::uuid AND status = 'open' AND occupancy < capacity`,
		shelterID,
	)
	if err != nil {
		return err
	}
	if ct.RowsAffected() > 0 {
		return nil
	}

	// Zero rows means the guard rejected us, but not why. Re-read to return a
	// precise reason — still inside the transaction, so it's a consistent view.
	var status string
	var occ, capacity int
	e := tx.QueryRow(ctx,
		`SELECT status, occupancy, capacity FROM shelters WHERE id = $1::uuid`, shelterID,
	).Scan(&status, &occ, &capacity)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrShelterNotFound
	}
	if e != nil {
		return e
	}
	if status != ShelterOpen {
		metrics.ReservationConflicts.WithLabelValues("shelter_bed", "closed").Inc()
		return ErrShelterClosed
	}
	metrics.ReservationConflicts.WithLabelValues("shelter_bed", "full").Inc()
	return ErrShelterFull
}

// List returns shelters, optionally filtered by status (empty = no filter),
// ordered by name, paginated.
func (r *ShelterRepository) List(ctx context.Context, status string, limit, offset int) ([]Shelter, error) {
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

	out := make([]Shelter, 0, limit)
	for rows.Next() {
		var sh Shelter
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
func (r *ShelterRepository) FindNearestOpen(ctx context.Context, lat, lng float64, limit int) ([]Shelter, error) {
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

	out := make([]Shelter, 0, limit)
	for rows.Next() {
		var sh Shelter
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
func (r *ShelterRepository) UpdateStatus(ctx context.Context, id, status string) (*Shelter, error) {
	const q = `
		UPDATE shelters SET status = $1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + shelterColumns
	var sh Shelter
	if err := scanShelter(r.pool.QueryRow(ctx, q, status, id), &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}
