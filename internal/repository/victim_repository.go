package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

// Assignment outcomes. The service translates these into its own sentinels.
var (
	ErrVictimNotFound        = errors.New("victim not found")
	ErrVictimAlreadyAssigned = errors.New("victim already assigned")
	ErrShelterNotFound       = errors.New("shelter not found")
	ErrShelterClosed         = errors.New("shelter closed")
	ErrShelterFull           = errors.New("shelter full")
)

type VictimRepository struct {
	pool *pgxpool.Pool
}

func NewVictimRepository(pool *pgxpool.Pool) *VictimRepository {
	return &VictimRepository{pool: pool}
}

// victimColumns projection; shelter_id::text is NULL until assigned (scans into
// the *string ShelterID). Order must match scanVictim.
const victimColumns = `id::text, name, status, notes, shelter_id::text,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanVictim(s scanner, v *models.Victim) error {
	return s.Scan(
		&v.ID, &v.Name, &v.Status, &v.Notes, &v.ShelterID,
		&v.Longitude, &v.Latitude, &v.CreatedAt, &v.UpdatedAt,
	)
}

// Create inserts a victim (status defaults to 'registered', shelter_id NULL).
// $3=lng, $4=lat.
func (r *VictimRepository) Create(ctx context.Context, v *models.Victim) error {
	const q = `
		INSERT INTO victims (name, notes, location)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		RETURNING id::text, status, shelter_id::text, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		v.Name, v.Notes, v.Longitude, v.Latitude,
	).Scan(&v.ID, &v.Status, &v.ShelterID, &v.CreatedAt, &v.UpdatedAt)
}

func (r *VictimRepository) GetByID(ctx context.Context, id string) (*models.Victim, error) {
	const q = `SELECT ` + victimColumns + ` FROM victims WHERE id = $1::uuid`
	var v models.Victim
	if err := scanVictim(r.pool.QueryRow(ctx, q, id), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// List returns victims, optionally filtered by status (empty = no filter),
// newest first, paginated.
func (r *VictimRepository) List(ctx context.Context, status string, limit, offset int) ([]models.Victim, error) {
	const q = `
		SELECT ` + victimColumns + ` FROM victims
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`
	rows, err := r.pool.Query(ctx, q, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Victim, 0, limit)
	for rows.Next() {
		var v models.Victim
		if err := scanVictim(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Assign places a victim into a shelter, atomically and safely. It guards TWO
// invariants at once, each with the right tool:
//
//   - the VICTIM is binary (assigned or not): lock it FOR UPDATE and re-check it's
//     still 'registered', so two operators can't place the same victim (P13 style).
//   - the SHELTER is a counter (must not overflow): a GUARDED conditional increment
//     — UPDATE ... WHERE status='open' AND occupancy < capacity. If it changes zero
//     rows, the shelter is full or closed. No SELECT ... FOR UPDATE is needed for
//     the counter: the conditional UPDATE self-serializes on the row write-lock and
//     re-checks occupancy under it, and the CHECK(occupancy<=capacity) is the
//     structural backstop.
//
// Lock order is always victim-then-shelter, so concurrent assignments can't deadlock.
func (r *VictimRepository) Assign(ctx context.Context, victimID, shelterID string) (*models.Victim, *models.Shelter, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// 1. Lock the victim, re-check it's unassigned (binary resource, P13 pattern).
	var vStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM victims WHERE id = $1::uuid FOR UPDATE`, victimID).Scan(&vStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrVictimNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if vStatus != models.VictimRegistered {
		return nil, nil, ErrVictimAlreadyAssigned
	}

	// 2. Guarded increment (counter resource, P18 pattern). Succeeds only if the
	//    shelter is open AND has room; the CHECK constraint is the backstop.
	ct, err := tx.Exec(ctx,
		`UPDATE shelters SET occupancy = occupancy + 1, updated_at = now()
		 WHERE id = $1::uuid AND status = 'open' AND occupancy < capacity`,
		shelterID,
	)
	if err != nil {
		return nil, nil, err
	}
	if ct.RowsAffected() == 0 {
		// Zero rows: figure out why so we can return a precise error.
		var sStatus string
		var occ, cap int
		e := tx.QueryRow(ctx, `SELECT status, occupancy, capacity FROM shelters WHERE id = $1::uuid`, shelterID).Scan(&sStatus, &occ, &cap)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, nil, ErrShelterNotFound
		}
		if e != nil {
			return nil, nil, e
		}
		if sStatus != models.ShelterOpen {
			return nil, nil, ErrShelterClosed
		}
		return nil, nil, ErrShelterFull
	}

	// 3. Assign the victim.
	var v models.Victim
	if err = scanVictim(
		tx.QueryRow(ctx,
			`UPDATE victims SET shelter_id = $1::uuid, status = 'sheltered', updated_at = now()
			 WHERE id = $2::uuid RETURNING `+victimColumns,
			shelterID, victimID,
		), &v,
	); err != nil {
		return nil, nil, err
	}

	// 4. Read the updated shelter to return (fresh occupancy/availableSpots).
	var sh models.Shelter
	if err = scanShelter(
		tx.QueryRow(ctx, `SELECT `+shelterColumns+` FROM shelters WHERE id = $1::uuid`, shelterID), &sh,
	); err != nil {
		return nil, nil, err
	}

	// 5. Emit the domain event in the same transaction (transactional outbox), so
	//    the event commits atomically with the assignment.
	if err = writeOutbox(ctx, tx, models.AggregateVictim, v.ID, models.EventVictimAssigned, map[string]any{
		"victimId":         v.ID,
		"shelterId":        sh.ID,
		"shelterOccupancy": sh.Occupancy,
	}); err != nil {
		return nil, nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &v, &sh, nil
}
