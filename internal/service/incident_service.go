// Package service holds business logic: validation, domain rules (like the
// incident status state machine), and orchestration across repositories. It sits
// between handlers (HTTP) and repositories (data).
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	ErrInvalidSeverity    = errors.New("invalid severity")
	ErrInvalidCoordinates = errors.New("coordinates out of range")
	ErrInvalidStatus      = errors.New("invalid status")
	ErrIllegalTransition  = errors.New("illegal status transition")
	ErrIncidentNotFound   = errors.New("incident not found")
	ErrInvalidRadius      = errors.New("radius must be between 1 and 100000 meters")
)

var validSeverities = map[string]bool{
	models.SeverityLow:      true,
	models.SeverityMedium:   true,
	models.SeverityHigh:     true,
	models.SeverityCritical: true,
}

// allowedTransitions is the incident status STATE MACHINE: from a status, the
// only legal next statuses are those listed. resolved and cancelled are terminal
// (empty slice = no outgoing transitions). This is the single source of truth for
// the lifecycle — trivially testable, no scattered if-checks.
var allowedTransitions = map[string][]string{
	models.StatusReported:   {models.StatusVerified, models.StatusCancelled},
	models.StatusVerified:   {models.StatusDispatched, models.StatusCancelled},
	models.StatusDispatched: {models.StatusResolved, models.StatusCancelled},
	models.StatusResolved:   {},
	models.StatusCancelled:  {},
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
	repo *repository.IncidentRepository
}

func NewIncidentService(repo *repository.IncidentRepository) *IncidentService {
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
func (s *IncidentService) Create(ctx context.Context, in CreateIncidentInput) (*models.Incident, bool, error) {
	if !validSeverities[in.Severity] {
		return nil, false, ErrInvalidSeverity
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, false, ErrInvalidCoordinates
	}

	// Dedupe first: merge into an existing active nearby-and-recent incident.
	if dup, err := s.repo.TryDedupe(ctx, in.Latitude, in.Longitude, dedupeRadiusMeters, dedupeWindowMinutes); err == nil {
		return dup, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("dedupe check: %w", err)
	}

	inc := &models.Incident{
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

func (s *IncidentService) GetByID(ctx context.Context, id string) (*models.Incident, error) {
	inc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, err
	}
	return inc, nil
}

func (s *IncidentService) List(ctx context.Context, limit, offset int) ([]models.Incident, error) {
	return s.repo.List(ctx, limit, offset)
}

// Nearby returns incidents within radiusMeters of (lat,lng), nearest first. This
// is the radius search backed by the GiST spatial index (P8).
func (s *IncidentService) Nearby(ctx context.Context, lat, lng, radiusMeters float64, limit int) ([]models.Incident, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, ErrInvalidCoordinates
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
func (s *IncidentService) UpdateStatus(ctx context.Context, id, newStatus string) (*models.Incident, error) {
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
