package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

var (
	ErrInvalidCapacity      = errors.New("capacity must be a positive integer")
	ErrInvalidShelterStatus = errors.New("invalid shelter status")
	ErrDuplicateShelterName = errors.New("shelter name already exists")
	ErrShelterNotFound      = errors.New("shelter not found")
)

var validShelterStatuses = map[string]bool{
	models.ShelterOpen:   true,
	models.ShelterClosed: true,
}

type ShelterService struct {
	repo *repository.ShelterRepository
}

func NewShelterService(repo *repository.ShelterRepository) *ShelterService {
	return &ShelterService{repo: repo}
}

type CreateShelterInput struct {
	Name      string
	Capacity  int
	Latitude  float64
	Longitude float64
}

// Create registers a new shelter (occupancy starts 0, status 'open'). Duplicate
// name is detected via the UNIQUE-violation error code (23505), race-free.
func (s *ShelterService) Create(ctx context.Context, in CreateShelterInput) (*models.Shelter, error) {
	if in.Capacity <= 0 {
		return nil, ErrInvalidCapacity
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, ErrInvalidCoordinates
	}

	sh := &models.Shelter{
		Name:      in.Name,
		Capacity:  in.Capacity,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
	}
	if err := s.repo.Create(ctx, sh); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateShelterName
		}
		return nil, fmt.Errorf("create shelter: %w", err)
	}
	return sh, nil
}

func (s *ShelterService) GetByID(ctx context.Context, id string) (*models.Shelter, error) {
	sh, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrShelterNotFound
		}
		return nil, err
	}
	return sh, nil
}

// List validates the optional status filter (empty = no filter) and returns shelters.
func (s *ShelterService) List(ctx context.Context, status string, limit, offset int) ([]models.Shelter, error) {
	if status != "" && !validShelterStatuses[status] {
		return nil, ErrInvalidShelterStatus
	}
	return s.repo.List(ctx, status, limit, offset)
}

// UpdateStatus is an admin open/closed toggle. Occupancy changes come from the
// P18 assignment flow, not here.
func (s *ShelterService) UpdateStatus(ctx context.Context, id, status string) (*models.Shelter, error) {
	if !validShelterStatuses[status] {
		return nil, ErrInvalidShelterStatus
	}
	sh, err := s.repo.UpdateStatus(ctx, id, status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrShelterNotFound
		}
		return nil, err
	}
	return sh, nil
}
