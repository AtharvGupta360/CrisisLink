package victim

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/dbx"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
)

// Assignment outcomes OWNED BY THIS MODULE. The shelter-side rejection reasons
// (not found / closed / full) deliberately live in the shelter module and are
// propagated unchanged — only shelter can decide what "full" means for its own
// capacity invariant. Duplicating them here is what caused an admission rejection
// to fall through to a 500 instead of a 409: two different error values meaning
// the same thing, and the handler was comparing against the wrong one.
var (
	ErrVictimNotFound        = errors.New("victim not found")
	ErrVictimAlreadyAssigned = errors.New("victim already assigned")
)

// ShelterAllocator is the SEAM the victim module depends on. It is declared HERE,
// by the consumer, not by shelter — dependency inversion, so victim states what it
// needs and shelter happens to satisfy it. Both methods take the caller's pgx.Tx so
// admission, the victim update and the outbox event all commit atomically while
// each module still owns its own tables.
//
// This interface is the "microservices-ready" boundary: to extract shelter into its
// own service, replace the implementation with a client (and the atomic transaction
// with a saga) and nothing in victim changes shape.
type ShelterAllocator interface {
	AdmitTx(ctx context.Context, tx pgx.Tx, shelterID string) error
	GetByIDTx(ctx context.Context, tx pgx.Tx, id string) (*shelter.Shelter, error)
}

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

type VictimRepository struct {
	pool     *pgxpool.Pool
	shelters ShelterAllocator
	events   EventWriter
}

func NewVictimRepository(pool *pgxpool.Pool, shelters ShelterAllocator, events EventWriter) *VictimRepository {
	return &VictimRepository{pool: pool, shelters: shelters, events: events}
}

// victimColumns projection; shelter_id::text is NULL until assigned (scans into
// the *string ShelterID). Order must match scanVictim.
const victimColumns = `id::text, name, status, notes, shelter_id::text,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanVictim(s dbx.Scanner, v *Victim) error {
	return s.Scan(
		&v.ID, &v.Name, &v.Status, &v.Notes, &v.ShelterID,
		&v.Longitude, &v.Latitude, &v.CreatedAt, &v.UpdatedAt,
	)
}

// Create inserts a victim (status defaults to 'registered', shelter_id NULL).
// $3=lng, $4=lat.
func (r *VictimRepository) Create(ctx context.Context, v *Victim) error {
	const q = `
		INSERT INTO victims (name, notes, location)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		RETURNING id::text, status, shelter_id::text, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		v.Name, v.Notes, v.Longitude, v.Latitude,
	).Scan(&v.ID, &v.Status, &v.ShelterID, &v.CreatedAt, &v.UpdatedAt)
}

func (r *VictimRepository) GetByID(ctx context.Context, id string) (*Victim, error) {
	const q = `SELECT ` + victimColumns + ` FROM victims WHERE id = $1::uuid`
	var v Victim
	if err := scanVictim(r.pool.QueryRow(ctx, q, id), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// List returns victims, optionally filtered by status (empty = no filter),
// newest first, paginated.
func (r *VictimRepository) List(ctx context.Context, status string, limit, offset int) ([]Victim, error) {
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

	out := make([]Victim, 0, limit)
	for rows.Next() {
		var v Victim
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
func (r *VictimRepository) Assign(ctx context.Context, victimID, shelterID string) (*Victim, *shelter.Shelter, error) {
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
	if vStatus != VictimRegistered {
		return nil, nil, ErrVictimAlreadyAssigned
	}

	// 2. Ask the shelter module to admit one person (the P18 no-overflow guard).
	//    We pass OUR transaction, so its increment commits with our victim update.
	if err := r.shelters.AdmitTx(ctx, tx, shelterID); err != nil {
		return nil, nil, err
	}

	// 3. Assign the victim.
	var v Victim
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
	// Read through the shelter module's SEAM, passing our transaction, rather than
	// querying the shelters table ourselves. Victim code must not know shelter's
	// column list or scan helper — that is shelter's private business.
	sh, err := r.shelters.GetByIDTx(ctx, tx, shelterID)
	if err != nil {
		return nil, nil, err
	}

	// 5. Emit the domain event in the same transaction (transactional outbox), so
	//    the event commits atomically with the assignment.
	if err = r.events.WriteTx(ctx, tx, outbox.AggregateVictim, v.ID, outbox.EventVictimAssigned, map[string]any{
		"victimId":         v.ID,
		"shelterId":        sh.ID,
		"shelterOccupancy": sh.Occupancy,
	}); err != nil {
		return nil, nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &v, sh, nil
}
