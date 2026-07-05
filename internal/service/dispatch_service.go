package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// DispatchService coordinates incidents and units. It grows across the dispatch
// phases: P11 candidate search (here), P12 scoring, P13 the reservation
// transaction. It depends on both repositories (it spans two aggregates).
type DispatchService struct {
	incidents *repository.IncidentRepository
	units     *repository.UnitRepository
}

func NewDispatchService(incidents *repository.IncidentRepository, units *repository.UnitRepository) *DispatchService {
	return &DispatchService{incidents: incidents, units: units}
}

// Candidates returns the incident and the nearest available units to it (KNN).
// P12 will score/rank these candidates; P13 will reserve the chosen one.
func (s *DispatchService) Candidates(ctx context.Context, incidentID string, limit int) (*models.Incident, []models.Unit, error) {
	inc, err := s.incidents.GetByID(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrIncidentNotFound
		}
		return nil, nil, err
	}

	if limit <= 0 || limit > 50 {
		limit = 5
	}
	units, err := s.units.FindNearestAvailable(ctx, inc.Latitude, inc.Longitude, "", limit)
	if err != nil {
		return nil, nil, err
	}
	return inc, units, nil
}
