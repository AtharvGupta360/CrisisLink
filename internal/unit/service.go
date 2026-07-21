package unit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

var (
	ErrInvalidUnitType   = errors.New("invalid unit type")
	ErrInvalidUnitStatus = errors.New("invalid unit status")
	ErrDuplicateCallSign = errors.New("call sign already exists")
	ErrUnitNotFound      = errors.New("unit not found")
)

var validUnitTypes = map[string]bool{
	UnitTypeAmbulance: true,
	UnitTypeFire:      true,
	UnitTypeRescue:    true,
	UnitTypePolice:    true,
}

var validUnitStatuses = map[string]bool{
	UnitAvailable:    true,
	UnitReserved:     true,
	UnitEnRoute:      true,
	UnitOnScene:      true,
	UnitOutOfService: true,
}

type UnitService struct {
	repo *UnitRepository
}

func NewUnitService(repo *UnitRepository) *UnitService {
	return &UnitService{repo: repo}
}

type CreateUnitInput struct {
	CallSign  string
	Type      string
	Latitude  float64
	Longitude float64
}

// Create registers a new unit (status starts 'available'). Duplicate call_sign is
// detected via the UNIQUE-violation error code (23505), race-free.
func (s *UnitService) Create(ctx context.Context, in CreateUnitInput) (*Unit, error) {
	if !validUnitTypes[in.Type] {
		return nil, ErrInvalidUnitType
	}
	if err := geo.ValidateLatLng(in.Latitude, in.Longitude); err != nil {
		return nil, err
	}

	u := &Unit{
		CallSign:  in.CallSign,
		Type:      in.Type,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
	}
	if err := s.repo.Create(ctx, u); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateCallSign
		}
		return nil, fmt.Errorf("create unit: %w", err)
	}
	return u, nil
}

func (s *UnitService) GetByID(ctx context.Context, id string) (*Unit, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnitNotFound
		}
		return nil, err
	}
	return u, nil
}

// List validates optional filters (empty = no filter) and returns units.
func (s *UnitService) List(ctx context.Context, status, unitType string, limit, offset int) ([]Unit, error) {
	if status != "" && !validUnitStatuses[status] {
		return nil, ErrInvalidUnitStatus
	}
	if unitType != "" && !validUnitTypes[unitType] {
		return nil, ErrInvalidUnitType
	}
	return s.repo.List(ctx, status, unitType, limit, offset)
}

// UpdateStatus is an admin override in P10 (any valid status). The disciplined
// dispatch lifecycle state machine arrives in P15.
func (s *UnitService) UpdateStatus(ctx context.Context, id, status string) (*Unit, error) {
	if !validUnitStatuses[status] {
		return nil, ErrInvalidUnitStatus
	}
	u, err := s.repo.UpdateStatus(ctx, id, status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnitNotFound
		}
		return nil, err
	}
	return u, nil
}
