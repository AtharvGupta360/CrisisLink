package shelter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

var (
	ErrInvalidCapacity      = errors.New("capacity must be a positive integer")
	ErrInvalidShelterStatus = errors.New("invalid shelter status")
	ErrDuplicateShelterName = errors.New("shelter name already exists")
	ErrShelterNotFound      = errors.New("shelter not found")
)

var validShelterStatuses = map[string]bool{
	ShelterOpen:   true,
	ShelterClosed: true,
}

// ShelterService owns the cache-aside logic. The cache deliberately lives HERE and
// not in the repository: the repository stays a pure Postgres adapter (easy to
// reason about, easy to reuse inside a transaction), and the read-through /
// invalidate sequence stays explicit at the layer that knows what a "write" means.
type ShelterService struct {
	repo  *ShelterRepository
	cache *ShelterCache
}

func NewShelterService(repo *ShelterRepository, c *ShelterCache) *ShelterService {
	return &ShelterService{repo: repo, cache: c}
}

type CreateShelterInput struct {
	Name      string
	Capacity  int
	Latitude  float64
	Longitude float64
}

// Create registers a new shelter (occupancy starts 0, status 'open'). Duplicate
// name is detected via the UNIQUE-violation error code (23505), race-free.
func (s *ShelterService) Create(ctx context.Context, in CreateShelterInput) (*Shelter, error) {
	if in.Capacity <= 0 {
		return nil, ErrInvalidCapacity
	}
	if err := geo.ValidateLatLng(in.Latitude, in.Longitude); err != nil {
		return nil, err
	}

	sh := &Shelter{
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

// GetByID reads through the cache. On a miss the loader below runs (deduplicated by
// singleflight), and only a SUCCESSFUL load is written back — a not-found is never
// cached, or one bad id would be served from Redis for a whole TTL.
func (s *ShelterService) GetByID(ctx context.Context, id string) (*Shelter, error) {
	sh, err := s.cache.GetOrLoad(ctx, id, func(ctx context.Context) (*Shelter, error) {
		sh, err := s.repo.GetByID(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrShelterNotFound
			}
			return nil, err
		}
		return sh, nil
	})
	if err != nil {
		return nil, err
	}
	return sh, nil
}

// List validates the optional status filter (empty = no filter) and returns shelters.
func (s *ShelterService) List(ctx context.Context, status string, limit, offset int) ([]Shelter, error) {
	if status != "" && !validShelterStatuses[status] {
		return nil, ErrInvalidShelterStatus
	}
	return s.repo.List(ctx, status, limit, offset)
}

// UpdateStatus is an admin open/closed toggle. Occupancy changes come from the
// P18 assignment flow, not here.
func (s *ShelterService) UpdateStatus(ctx context.Context, id, status string) (*Shelter, error) {
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

	// Invalidate AFTER Postgres has committed, and DELETE rather than overwrite with
	// sh — two concurrent status flips could reach Redis in the reverse of the order
	// they reached Postgres, and the loser's SET would win permanently. DEL cannot
	// lose that race: the next reader rebuilds from the source of truth.
	s.cache.Invalidate(ctx, id)
	return sh, nil
}
