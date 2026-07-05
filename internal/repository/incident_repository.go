package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

type IncidentRepository struct {
	pool *pgxpool.Pool
}

func NewIncidentRepository(pool *pgxpool.Pool) *IncidentRepository {
	return &IncidentRepository{pool: pool}
}

// scanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query loop), so
// one scan helper serves single-row and multi-row reads.
type scanner interface {
	Scan(dest ...any) error
}

// incidentColumns is the shared projection. ST_X = longitude, ST_Y = latitude —
// the column ORDER here must match scanIncident's Scan order exactly.
const incidentColumns = `id::text, reporter_id::text, title, description, severity, status,
	ST_X(location) AS longitude, ST_Y(location) AS latitude, created_at, updated_at`

func scanIncident(s scanner, inc *models.Incident) error {
	return s.Scan(
		&inc.ID, &inc.ReporterID, &inc.Title, &inc.Description,
		&inc.Severity, &inc.Status, &inc.Longitude, &inc.Latitude,
		&inc.CreatedAt, &inc.UpdatedAt,
	)
}

// Create inserts an incident. The location is built from lng/lat via ST_MakePoint
// — NOTE the argument order is ($lng, $lat): X then Y. status defaults to
// 'reported' in the DB. Fills in the generated fields via RETURNING.
func (r *IncidentRepository) Create(ctx context.Context, inc *models.Incident) error {
	const q = `
		INSERT INTO incidents (reporter_id, title, description, severity, location)
		VALUES ($1::uuid, $2, $3, $4, ST_SetSRID(ST_MakePoint($5, $6), 4326))
		RETURNING id::text, status, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		inc.ReporterID, inc.Title, inc.Description, inc.Severity,
		inc.Longitude, inc.Latitude, // $5 = lng, $6 = lat
	).Scan(&inc.ID, &inc.Status, &inc.CreatedAt, &inc.UpdatedAt)
}

// GetByID returns one incident. Returns pgx.ErrNoRows if not found (the service
// translates that to a domain not-found error).
func (r *IncidentRepository) GetByID(ctx context.Context, id string) (*models.Incident, error) {
	const q = `SELECT ` + incidentColumns + ` FROM incidents WHERE id = $1::uuid`
	var inc models.Incident
	if err := scanIncident(r.pool.QueryRow(ctx, q, id), &inc); err != nil {
		return nil, err
	}
	return &inc, nil
}

// List returns incidents newest-first, paginated.
func (r *IncidentRepository) List(ctx context.Context, limit, offset int) ([]models.Incident, error) {
	const q = `SELECT ` + incidentColumns + ` FROM incidents ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	rows, err := r.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Incident, 0, limit)
	for rows.Next() {
		var inc models.Incident
		if err := scanIncident(rows, &inc); err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// UpdateStatus sets a new status (the caller/service has already validated the
// transition) and returns the updated row.
func (r *IncidentRepository) UpdateStatus(ctx context.Context, id, status string) (*models.Incident, error) {
	const q = `
		UPDATE incidents SET status = $1, updated_at = now()
		WHERE id = $2::uuid
		RETURNING ` + incidentColumns
	var inc models.Incident
	if err := scanIncident(r.pool.QueryRow(ctx, q, status, id), &inc); err != nil {
		return nil, err
	}
	return &inc, nil
}
