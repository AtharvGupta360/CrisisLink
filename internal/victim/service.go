package victim

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
)

// VictimService coordinates victims and shelters. Like DispatchService (incidents
// + units), it spans two aggregates: it registers victims and finds shelters for
// them, and in P18 will assign a victim to a shelter under a capacity guard.
type VictimService struct {
	victims  *VictimRepository
	shelters *shelter.ShelterRepository

	// Assigning a victim increments a SHELTER's occupancy, so this service dirties
	// the shelter cache even though it owns no shelter reads. This is the part people
	// get wrong in practice: an entity's cache must be invalidated by EVERY writer of
	// that entity, wherever that writer lives. Miss one call site and you have a
	// stale key with no invalidation path — the bug that made "cache invalidation"
	// proverbial. Same *ShelterCache instance as ShelterService, injected in server.go.
	shelterCache *shelter.ShelterCache
}

func NewVictimService(victims *VictimRepository, shelters *shelter.ShelterRepository, shelterCache *shelter.ShelterCache) *VictimService {
	return &VictimService{victims: victims, shelters: shelters, shelterCache: shelterCache}
}

type CreateVictimInput struct {
	Name      string
	Notes     string
	Latitude  float64
	Longitude float64
}

// Create registers a victim (status starts 'registered', unassigned).
func (s *VictimService) Create(ctx context.Context, in CreateVictimInput) (*Victim, error) {
	if err := geo.ValidateLatLng(in.Latitude, in.Longitude); err != nil {
		return nil, err
	}
	v := &Victim{
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

func (s *VictimService) GetByID(ctx context.Context, id string) (*Victim, error) {
	v, err := s.victims.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVictimNotFound
		}
		return nil, err
	}
	return v, nil
}

func (s *VictimService) List(ctx context.Context, status string, limit, offset int) ([]Victim, error) {
	return s.victims.List(ctx, status, limit, offset)
}

// NearestShelters returns the victim and the nearest open shelters with room (KNN).
// P18 will assign the victim to a chosen one.
func (s *VictimService) NearestShelters(ctx context.Context, victimID string, limit int) (*Victim, []shelter.Shelter, error) {
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
func (s *VictimService) Assign(ctx context.Context, victimID, shelterID string) (*Victim, *shelter.Shelter, error) {
	v, sh, err := s.victims.Assign(ctx, victimID, shelterID)
	if err != nil {
		// Repository sentinels are already this module's public vocabulary (they are
		// declared in repository.go), and shelter's admission errors propagate as-is —
		// the handler maps both to HTTP codes.
		return nil, nil, err
	}

	// The assign transaction has COMMITTED and occupancy went up, so the cached copy
	// of this shelter is now stale. Invalidate on the success path only: a rejected
	// assign (full/closed/already-assigned) rolled back and changed nothing, so there
	// is nothing to invalidate.
	//
	// Note what we are NOT doing: caching occupancy for the capacity check. The
	// no-overflow guard is still `UPDATE ... WHERE occupancy < capacity` inside
	// Postgres, in the same transaction as the victim update. A stale cache can make
	// a dispatcher's screen wrong for up to a TTL; it can never let us admit a victim
	// into a full shelter. Correctness never reads from the cache.
	s.shelterCache.Invalidate(ctx, shelterID)
	return v, sh, nil
}
