package dispatch

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/presence"
	"github.com/AtharvGupta360/CrisisLink/internal/scoring"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

// Sentinels the service adds on top of the repository's (which are declared in
// repository.go and are now this module's shared vocabulary — before the modular
// split the two lived in different packages and each needed its own copy).
var (
	ErrUnitUnavailable       = errors.New("unit is not available for dispatch")
	ErrInvalidDispatchStatus = errors.New("invalid dispatch status")
)

// ReserveStrategy selects the concurrency-control approach for a reservation.
// Pessimistic (P13) locks the unit row up front; optimistic (P14) uses a version
// compare-and-swap with retries. Same guarantee (no double-booking), different
// mechanics — the endpoint exposes both so they can be compared.
type ReserveStrategy string

const (
	StrategyPessimistic ReserveStrategy = "pessimistic"
	StrategyOptimistic  ReserveStrategy = "optimistic"
)

// DispatchService coordinates incidents, units, and dispatches. It grows across
// the dispatch phases: P11 candidate search, P12 scoring, P13 the reservation
// transaction (below). It spans three aggregates.
// LiveLocator is the presence SEAM. Dispatch wants to rank units by where they
// ACTUALLY are, not where they were registered, but it must not know that live
// positions happen to live in a Redis sorted set.
//
// Note this seam takes no pgx.Tx: presence is not part of the reservation
// transaction and must never be. It informs the CHOICE of unit; the reservation
// itself is still decided entirely by Postgres, because Redis cannot be rolled back
// and cannot be trusted to enforce an invariant.
type LiveLocator interface {
	NearbyLive(ctx context.Context, lat, lng, radiusMeters float64, limit int) ([]presence.NearbyUnit, error)
}

// liveSearchRadiusMeters bounds the GEO lookup. Wide enough to cover a city-scale
// response, narrow enough that a far-flung unit is not proposed for a local
// incident. Units beyond it fall to the registry path.
const liveSearchRadiusMeters = 20000

type DispatchService struct {
	incidents  *incident.IncidentRepository
	units      *unit.UnitRepository
	dispatches *DispatchRepository
	live       LiveLocator
}

func NewDispatchService(incidents *incident.IncidentRepository, units *unit.UnitRepository, dispatches *DispatchRepository, live LiveLocator) *DispatchService {
	return &DispatchService{incidents: incidents, units: units, dispatches: dispatches, live: live}
}

// Reserve assigns a specific unit to an incident via the no-double-booking
// transaction, using the chosen concurrency-control strategy. It delegates the
// atomic work to the repository and translates the repo's outcomes into service
// sentinels the handler maps to HTTP codes.
func (s *DispatchService) Reserve(ctx context.Context, incidentID, unitID string, strategy ReserveStrategy) (*Dispatch, error) {
	var (
		d   *Dispatch
		err error
	)
	switch strategy {
	case StrategyOptimistic:
		d, err = s.dispatches.ReserveOptimistic(ctx, incidentID, unitID)
	default: // pessimistic is the production default
		d, err = s.dispatches.Reserve(ctx, incidentID, unitID)
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrUnitNotFound):
			return nil, unit.ErrUnitNotFound
		case errors.Is(err, ErrUnitNotAvailable):
			return nil, ErrUnitUnavailable
		case errors.Is(err, ErrIncidentNotFound):
			return nil, incident.ErrIncidentNotFound
		case errors.Is(err, ErrIncidentNotDispatchable):
			return nil, ErrIncidentNotDispatchable
		case errors.Is(err, ErrReservationConflict):
			return nil, ErrReservationConflict
		default:
			return nil, err
		}
	}
	return d, nil
}

// GetDispatch returns a single dispatch by id.
func (s *DispatchService) GetDispatch(ctx context.Context, id string) (*Dispatch, error) {
	d, err := s.dispatches.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDispatchNotFound
		}
		return nil, err
	}
	return d, nil
}

// ListIncidentDispatches returns all dispatches for an incident.
func (s *DispatchService) ListIncidentDispatches(ctx context.Context, incidentID string) ([]Dispatch, error) {
	return s.dispatches.ListByIncident(ctx, incidentID)
}

// AdvanceStatus moves a dispatch along its lifecycle (validates the target status,
// then delegates the atomic transition + unit/incident sync to the repository).
func (s *DispatchService) AdvanceStatus(ctx context.Context, dispatchID, newStatus string) (*Dispatch, error) {
	if !IsValidDispatchStatus(newStatus) {
		return nil, ErrInvalidDispatchStatus
	}
	d, err := s.dispatches.AdvanceStatus(ctx, dispatchID, newStatus)
	if err != nil {
		switch {
		case errors.Is(err, ErrDispatchNotFound):
			return nil, ErrDispatchNotFound
		case errors.Is(err, ErrIllegalDispatchTransition):
			return nil, ErrIllegalDispatchTransition
		default:
			return nil, err
		}
	}
	return d, nil
}

// Candidates returns the incident and its dispatch candidates: the nearest
// available units (KNN shortlist), each scored and sorted best-first. preferredType
// ("" = no preference) lets a slightly-farther unit of the ideal kind outrank a
// closer wrong-type unit. P13 will reserve the top-scored candidate.
//
// Two-stage design on purpose: the DB does the cheap geometric shortlisting (KNN
// picks the k nearest via the spatial index), then the pure scoring function ranks
// that small set in Go. We never score the whole fleet — only the shortlist.
// PositionSource records WHERE the distances in a candidate list came from. It is
// returned to the caller because an "explainable" dispatch engine must not quietly
// switch between live and stale data — the operator deserves to know which they are
// looking at.
const (
	PositionSourceLive     = "live"     // Redis GEO: real heartbeat positions
	PositionSourceRegistry = "registry" // PostGIS KNN: registration pins, possibly stale
)

// Candidates returns the incident plus scored, ranked units to dispatch.
//
// HYBRID SEARCH. Two stores hold positions and neither is sufficient alone:
//
//   - Redis GEO has LIVE positions but no attributes (status, type) and is
//     ephemeral — after a restart it is empty.
//   - PostGIS has attributes and durability but only the REGISTRATION pin, so a
//     unit that has driven across town still looks like it never moved.
//
// So: ask Redis who is physically near right now, hydrate those ids from Postgres
// (which enforces status='available'), and rank by the LIVE distance. If Redis is
// empty, errors, or nothing live is in range, FALL BACK to the PostGIS KNN — the
// answer is then merely stale rather than absent. A position cache being down must
// degrade dispatch, never stop it.
func (s *DispatchService) Candidates(ctx context.Context, incidentID, preferredType string, limit int) (*incident.Incident, []scoring.ScoredUnit, string, error) {
	inc, err := s.incidents.GetByID(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, "", incident.ErrIncidentNotFound
		}
		return nil, nil, "", err
	}

	if limit <= 0 || limit > 50 {
		limit = 5
	}

	units, source, err := s.candidateUnits(ctx, inc, limit)
	if err != nil {
		return nil, nil, "", err
	}

	scored := make([]scoring.ScoredUnit, 0, len(units))
	for i := range units {
		score, bd := scoring.Score(&units[i], preferredType)
		scored = append(scored, scoring.ScoredUnit{Unit: units[i], Score: score, Breakdown: bd})
	}
	// Stable sort by score descending: ties keep the incoming (nearest-first) order.
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	return inc, scored, source, nil
}

// candidateUnits runs the live path, falling back to the registry path.
func (s *DispatchService) candidateUnits(ctx context.Context, inc *incident.Incident, limit int) ([]unit.Unit, string, error) {
	if s.live != nil {
		// Over-fetch: some live units will be busy and get filtered out by the
		// availability check below, so asking for exactly `limit` would come up short.
		nearby, err := s.live.NearbyLive(ctx, inc.Latitude, inc.Longitude, liveSearchRadiusMeters, limit*3)
		if err != nil {
			common.Logger.Warnw("live position lookup failed, falling back to registry positions",
				"incidentId", inc.ID, "error", err)
		} else if len(nearby) > 0 {
			if units, ok := s.hydrateLive(ctx, nearby, limit); ok {
				return units, PositionSourceLive, nil
			}
		}
	}

	// Fallback: nearest available by registration pin (the P11 KNN).
	units, err := s.units.FindNearestAvailable(ctx, inc.Latitude, inc.Longitude, "", limit)
	if err != nil {
		return nil, "", err
	}
	return units, PositionSourceRegistry, nil
}

// hydrateLive turns live GEO hits into full units, keeping Redis's distance and
// ordering. Reports false when nothing usable survived, so the caller can fall back.
func (s *DispatchService) hydrateLive(ctx context.Context, nearby []presence.NearbyUnit, limit int) ([]unit.Unit, bool) {
	ids := make([]string, len(nearby))
	for i, n := range nearby {
		ids[i] = n.UnitID
	}
	rows, err := s.units.FindAvailableByIDs(ctx, ids)
	if err != nil {
		common.Logger.Warnw("could not hydrate live candidates, falling back", "error", err)
		return nil, false
	}
	if len(rows) == 0 {
		return nil, false // everyone nearby is busy; let the registry path try wider
	}

	byID := make(map[string]unit.Unit, len(rows))
	for _, u := range rows {
		byID[u.ID] = u
	}

	// Walk `nearby` (already nearest-first from Redis) so live ordering is preserved,
	// and overwrite the position with the LIVE one — otherwise scoring would rank on
	// the stale registration distance and the whole exercise would be pointless.
	out := make([]unit.Unit, 0, limit)
	for _, n := range nearby {
		u, ok := byID[n.UnitID]
		if !ok {
			continue // not available
		}
		u.Latitude = n.Latitude
		u.Longitude = n.Longitude
		u.DistanceMeters = n.DistanceMeters
		out = append(out, u)
		if len(out) == limit {
			break
		}
	}
	return out, len(out) > 0
}
