package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

var (
	ErrVictimNotFound        = errors.New("victim not found")
	ErrVictimAlreadyAssigned = errors.New("victim already assigned to a shelter")
	ErrShelterClosed         = errors.New("shelter is closed")
	ErrShelterFull           = errors.New("shelter is full")
)

// VictimService coordinates victims and shelters. Like DispatchService (incidents
// + units), it spans two aggregates: it registers victims and finds shelters for
// them, and in P18 will assign a victim to a shelter under a capacity guard.
type VictimService struct {
	victims  *repository.VictimRepository
	shelters *repository.ShelterRepository
}

func NewVictimService(victims *repository.VictimRepository, shelters *repository.ShelterRepository) *VictimService {
	return &VictimService{victims: victims, shelters: shelters}
}

type CreateVictimInput struct {
	Name      string
	Notes     string
	Latitude  float64
	Longitude float64
}

// Create registers a victim (status starts 'registered', unassigned).
func (s *VictimService) Create(ctx context.Context, in CreateVictimInput) (*models.Victim, error) {
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, ErrInvalidCoordinates
	}
	v := &models.Victim{
		Name:      in.Name,
		Notes:     in.Notes,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
	}
	if err := s.victims.Create(ctx, v); err != nil {
		return nil, fmt.Errorf("create victim: %w", err)
	}
	return v, nil
}

func (s *VictimService) GetByID(ctx context.Context, id string) (*models.Victim, error) {
	v, err := s.victims.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVictimNotFound
		}
		return nil, err
	}
	return v, nil
}

func (s *VictimService) List(ctx context.Context, status string, limit, offset int) ([]models.Victim, error) {
	return s.victims.List(ctx, status, limit, offset)
}

// NearestShelters returns the victim and the nearest open shelters with room (KNN).
// P18 will assign the victim to a chosen one.
func (s *VictimService) NearestShelters(ctx context.Context, victimID string, limit int) (*models.Victim, []models.Shelter, error) {
	v, err := s.victims.GetByID(ctx, victimID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrVictimNotFound
		}
		return nil, nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	shelters, err := s.shelters.FindNearestOpen(ctx, v.Latitude, v.Longitude, limit)
	if err != nil {
		return nil, nil, err
	}
	return v, shelters, nil
}

// Assign places a victim into a shelter via the no-overflow transaction, and
// returns the updated victim + shelter. Translates repo outcomes into service
// sentinels the handler maps to HTTP codes.
func (s *VictimService) Assign(ctx context.Context, victimID, shelterID string) (*models.Victim, *models.Shelter, error) {
	v, sh, err := s.victims.Assign(ctx, victimID, shelterID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrVictimNotFound):
			return nil, nil, ErrVictimNotFound
		case errors.Is(err, repository.ErrVictimAlreadyAssigned):
			return nil, nil, ErrVictimAlreadyAssigned
		case errors.Is(err, repository.ErrShelterNotFound):
			return nil, nil, ErrShelterNotFound
		case errors.Is(err, repository.ErrShelterClosed):
			return nil, nil, ErrShelterClosed
		case errors.Is(err, repository.ErrShelterFull):
			return nil, nil, ErrShelterFull
		default:
			return nil, nil, err
		}
	}
	return v, sh, nil
}
