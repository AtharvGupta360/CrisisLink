package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

var (
	ErrInvalidCapacity   = errors.New("capacity must be a positive integer")
	ErrInvalidKind       = errors.New("invalid transport kind")
	ErrInvalidStatus     = errors.New("invalid transport status")
	ErrInvalidSeats      = errors.New("seats must be a positive integer")
	ErrDuplicateCallSign = errors.New("call sign already exists")
)

var validKinds = map[string]bool{
	KindBus: true, KindBoat: true, KindHelicopter: true, KindTruck: true,
}

var validStatuses = map[string]bool{
	StatusAvailable: true, StatusInService: true, StatusOutOfService: true,
}

type TransportService struct {
	repo *TransportRepository
}

func NewTransportService(repo *TransportRepository) *TransportService {
	return &TransportService{repo: repo}
}

type CreateInput struct {
	CallSign  string
	Kind      string
	Capacity  int
	Latitude  float64
	Longitude float64
}

func (s *TransportService) Create(ctx context.Context, in CreateInput) (*Transport, error) {
	if !validKinds[in.Kind] {
		return nil, ErrInvalidKind
	}
	if in.Capacity <= 0 {
		return nil, ErrInvalidCapacity
	}
	if err := geo.ValidateLatLng(in.Latitude, in.Longitude); err != nil {
		return nil, err
	}

	t := &Transport{
		CallSign: in.CallSign, Kind: in.Kind, Capacity: in.Capacity,
		Latitude: in.Latitude, Longitude: in.Longitude,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		// Race-free duplicate detection via the UNIQUE violation code, rather than
		// a check-then-insert which two concurrent creates could both pass.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateCallSign
		}
		return nil, fmt.Errorf("create transport: %w", err)
	}
	return t, nil
}

func (s *TransportService) GetByID(ctx context.Context, id string) (*Transport, error) {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransportNotFound
		}
		return nil, err
	}
	return t, nil
}

func (s *TransportService) List(ctx context.Context, status, kind string, limit, offset int) ([]Transport, error) {
	if status != "" && !validStatuses[status] {
		return nil, ErrInvalidStatus
	}
	if kind != "" && !validKinds[kind] {
		return nil, ErrInvalidKind
	}
	return s.repo.List(ctx, status, kind, limit, offset)
}

// FindNearest returns vehicles near (lat,lng) that can seat the requested group.
// seats is part of the QUERY, not a post-filter: asking the database for "vehicles
// that fit 5" is what lets the partial index do the work.
func (s *TransportService) FindNearest(ctx context.Context, lat, lng float64, seats, limit int) ([]Transport, error) {
	if seats <= 0 {
		return nil, ErrInvalidSeats
	}
	if err := geo.ValidateLatLng(lat, lng); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	return s.repo.FindNearestAvailable(ctx, lat, lng, seats, limit)
}

// Book claims seats. Repository sentinels are already this module's vocabulary, so
// they propagate unchanged and the handler maps them to HTTP codes.
func (s *TransportService) Book(ctx context.Context, transportID, incidentID string, seats int) (*Booking, *Transport, error) {
	if seats <= 0 {
		return nil, nil, ErrInvalidSeats
	}
	return s.repo.BookSeats(ctx, transportID, incidentID, seats)
}

func (s *TransportService) Cancel(ctx context.Context, bookingID string) (*Booking, *Transport, error) {
	return s.repo.CancelBooking(ctx, bookingID)
}

func (s *TransportService) ListBookings(ctx context.Context, incidentID string, limit int) ([]Booking, error) {
	return s.repo.ListBookings(ctx, incidentID, limit)
}
