package service

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
	"github.com/AtharvGupta360/CrisisLink/internal/scoring"
)

// ErrUnitUnavailable and ErrIncidentNotDispatchable are the reservation-specific
// business outcomes surfaced to the handler (mapped to 409 Conflict). The repo
// signals these; the service re-expresses them as its own sentinels.
var (
	ErrUnitUnavailable         = errors.New("unit is not available for dispatch")
	ErrIncidentNotDispatchable = errors.New("incident cannot be dispatched")
	// ErrReservationConflict surfaces the optimistic path exhausting its retries.
	ErrReservationConflict = errors.New("reservation conflicted, please retry")

	// P15 lifecycle sentinels.
	ErrDispatchNotFound          = errors.New("dispatch not found")
	ErrInvalidDispatchStatus     = errors.New("invalid dispatch status")
	ErrIllegalDispatchTransition = errors.New("illegal dispatch status transition")
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
	incidents  *repository.IncidentRepository
	units      *repository.UnitRepository
	dispatches *repository.DispatchRepository
}

func NewDispatchService(incidents *repository.IncidentRepository, units *repository.UnitRepository, dispatches *repository.DispatchRepository) *DispatchService {
	return &DispatchService{incidents: incidents, units: units, dispatches: dispatches}
}

// Reserve assigns a specific unit to an incident via the no-double-booking
// transaction, using the chosen concurrency-control strategy. It delegates the
// atomic work to the repository and translates the repo's outcomes into service
// sentinels the handler maps to HTTP codes.
func (s *DispatchService) Reserve(ctx context.Context, incidentID, unitID string, strategy ReserveStrategy) (*models.Dispatch, error) {
	var (
		d   *models.Dispatch
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
		case errors.Is(err, repository.ErrUnitNotFound):
			return nil, ErrUnitNotFound
		case errors.Is(err, repository.ErrUnitNotAvailable):
			return nil, ErrUnitUnavailable
		case errors.Is(err, repository.ErrIncidentNotFound):
			return nil, ErrIncidentNotFound
		case errors.Is(err, repository.ErrIncidentNotDispatchable):
			return nil, ErrIncidentNotDispatchable
		case errors.Is(err, repository.ErrReservationConflict):
			return nil, ErrReservationConflict
		default:
			return nil, err
		}
	}
	return d, nil
}

// GetDispatch returns a single dispatch by id.
func (s *DispatchService) GetDispatch(ctx context.Context, id string) (*models.Dispatch, error) {
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
func (s *DispatchService) ListIncidentDispatches(ctx context.Context, incidentID string) ([]models.Dispatch, error) {
	return s.dispatches.ListByIncident(ctx, incidentID)
}

// AdvanceStatus moves a dispatch along its lifecycle (validates the target status,
// then delegates the atomic transition + unit/incident sync to the repository).
func (s *DispatchService) AdvanceStatus(ctx context.Context, dispatchID, newStatus string) (*models.Dispatch, error) {
	if !models.IsValidDispatchStatus(newStatus) {
		return nil, ErrInvalidDispatchStatus
	}
	d, err := s.dispatches.AdvanceStatus(ctx, dispatchID, newStatus)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrDispatchNotFound):
			return nil, ErrDispatchNotFound
		case errors.Is(err, repository.ErrIllegalDispatchTransition):
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
func (s *DispatchService) Candidates(ctx context.Context, incidentID, preferredType string, limit int) (*models.Incident, []scoring.ScoredUnit, error) {
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
