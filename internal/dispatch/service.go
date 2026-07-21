package dispatch

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
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
type DispatchService struct {
	incidents  *incident.IncidentRepository
	units      *unit.UnitRepository
	dispatches *DispatchRepository
}

func NewDispatchService(incidents *incident.IncidentRepository, units *unit.UnitRepository, dispatches *DispatchRepository) *DispatchService {
	return &DispatchService{incidents: incidents, units: units, dispatches: dispatches}
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
func (s *DispatchService) Candidates(ctx context.Context, incidentID, preferredType string, limit int) (*incident.Incident, []scoring.ScoredUnit, error) {
	inc, err := s.incidents.GetByID(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, incident.ErrIncidentNotFound
		}
		return nil, nil, err
	}

	if limit <= 0 || limit > 50 {
		limit = 5
	}
	// KNN shortlist stays type-agnostic: we still want the nearest AVAILABLE units
	// regardless of type, then let scoring decide how much the type gap costs.
	units, err := s.units.FindNearestAvailable(ctx, inc.Latitude, inc.Longitude, "", limit)
	if err != nil {
		return nil, nil, err
	}

	scored := make([]scoring.ScoredUnit, 0, len(units))
	for i := range units {
		score, bd := scoring.Score(&units[i], preferredType)
		scored = append(scored, scoring.ScoredUnit{Unit: units[i], Score: score, Breakdown: bd})
	}
	// Stable sort by score descending: ties keep KNN (nearest-first) order.
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	return inc, scored, nil
}
