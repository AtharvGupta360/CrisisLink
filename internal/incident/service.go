package incident

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	ErrInvalidSeverity   = errors.New("invalid severity")
	ErrInvalidStatus     = errors.New("invalid status")
	ErrIllegalTransition = errors.New("illegal status transition")
	ErrIncidentNotFound  = errors.New("incident not found")
	ErrInvalidRadius     = errors.New("radius must be between 1 and 100000 meters")
)

var validSeverities = map[string]bool{
	SeverityLow:      true,
	SeverityMedium:   true,
	SeverityHigh:     true,
	SeverityCritical: true,
}

// allowedTransitions is the incident status STATE MACHINE: from a status, the
// only legal next statuses are those listed. resolved and cancelled are terminal
// (empty slice = no outgoing transitions). This is the single source of truth for
// the lifecycle — trivially testable, no scattered if-checks.
var allowedTransitions = map[string][]string{
	StatusReported:   {StatusVerified, StatusCancelled},
	StatusVerified:   {StatusDispatched, StatusCancelled},
	StatusDispatched: {StatusResolved, StatusCancelled},
	StatusResolved:   {},
	StatusCancelled:  {},
}

// canTransition reports whether from -> to is a legal move in the state machine.
func canTransition(from, to string) bool {
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

type IncidentService struct {
	repo *IncidentRepository
}

func NewIncidentService(repo *IncidentRepository) *IncidentService {
	return &IncidentService{repo: repo}
}

// CreateIncidentInput is the validated input for reporting an incident.
type CreateIncidentInput struct {
	ReporterID  string
	Title       string
	Description string
	Severity    string
	Latitude    float64
	Longitude   float64
}

// Dedupe window: a report within this radius (meters) and time of an ACTIVE
// incident is treated as another report of the SAME event, not a new incident.
// Tuned tight on purpose — a false merge hides a distinct event (dangerous in a
// life-safety system); a missed duplicate only adds noise. Single tunable place.
const (
	dedupeRadiusMeters  = 100.0
	dedupeWindowMinutes = 15
)

// Create validates, then deduplicates. If an active incident was reported nearby
// and recently, this report is merged into it (report_count incremented) and
// returned with deduped=true. Otherwise a new incident is created (deduped=false).
// Validation here is defense-in-depth: the handler validates too, but the service
// must not trust its caller.
func (s *IncidentService) Create(ctx context.Context, in CreateIncidentInput) (*Incident, bool, error) {
	if !validSeverities[in.Severity] {
		return nil, false, ErrInvalidSeverity
	}
	if err := geo.ValidateLatLng(in.Latitude, in.Longitude); err != nil {
		return nil, false, err
	}

	// Dedupe first: merge into an existing active nearby-and-recent incident.
	if dup, err := s.repo.TryDedupe(ctx, in.Latitude, in.Longitude, dedupeRadiusMeters, dedupeWindowMinutes); err == nil {
		return dup, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("dedupe check: %w", err)
	}

	inc := &Incident{
		ReporterID:  in.ReporterID,
		Title:       in.Title,
		Description: in.Description,
		Severity:    in.Severity,
		Latitude:    in.Latitude,
		Longitude:   in.Longitude,
	}
	if err := s.repo.Create(ctx, inc); err != nil {
		return nil, false, fmt.Errorf("create incident: %w", err)
	}
	return inc, false, nil
}

func (s *IncidentService) GetByID(ctx context.Context, id string) (*Incident, error) {
	inc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, err
	}
	return inc, nil
}

func (s *IncidentService) List(ctx context.Context, limit, offset int) ([]Incident, error) {
	return s.repo.List(ctx, limit, offset)
}

// Nearby returns incidents within radiusMeters of (lat,lng), nearest first. This
// is the radius search backed by the GiST spatial index (P8).
func (s *IncidentService) Nearby(ctx context.Context, lat, lng, radiusMeters float64, limit int) ([]Incident, error) {
	if err := geo.ValidateLatLng(lat, lng); err != nil {
		return nil, err
	}
	if radiusMeters <= 0 || radiusMeters > 100000 {
		return nil, ErrInvalidRadius
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.repo.FindWithinRadius(ctx, lat, lng, radiusMeters, limit)
}

// UpdateStatus enforces the state machine: it loads the current status and
// rejects any transition not permitted by allowedTransitions.
func (s *IncidentService) UpdateStatus(ctx context.Context, id, newStatus string) (*Incident, error) {
	if _, ok := allowedTransitions[newStatus]; !ok {
		return nil, ErrInvalidStatus
	}

	inc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, err
	}

	if !canTransition(inc.Status, newStatus) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, inc.Status, newStatus)
	}

	return s.repo.UpdateStatus(ctx, id, newStatus)
}
