// Package transport manages evacuation vehicles and the seats booked on them.
//
// It is the third resource shape in the platform, and the one that generalises the
// other two. A rescue unit is BOOLEAN (reserved or not). A shelter is a COUNTER
// incremented by one. A transport is a counter incremented by N — you book seats
// for a whole family at once — which turns the capacity guard into a quantity
// comparison and forces an all-or-nothing decision.
package transport

import "time"

// Vehicle kinds. Different kinds matter operationally (a boat reaches a flooded
// street a bus cannot) and are surfaced so a dispatcher can choose.
const (
	KindBus        = "bus"
	KindBoat       = "boat"
	KindHelicopter = "helicopter"
	KindTruck      = "truck"
)

// Transport statuses. Only 'available' vehicles can take new bookings; in_service
// means it is currently running an evacuation, out_of_service is maintenance.
const (
	StatusAvailable    = "available"
	StatusInService    = "in_service"
	StatusOutOfService = "out_of_service"
)

// Booking statuses.
const (
	BookingBooked    = "booked"
	BookingCompleted = "completed"
	BookingCancelled = "cancelled"
)

// Transport is an evacuation vehicle with a finite number of seats.
type Transport struct {
	ID         string    `json:"id"`
	CallSign   string    `json:"callSign"`
	Kind       string    `json:"kind"`
	Capacity   int       `json:"capacity"`
	SeatsTaken int       `json:"seatsTaken"`
	Status     string    `json:"status"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	Version    int       `json:"version"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`

	// SeatsFree is derived (capacity - seats_taken) on read, never stored, so it
	// can never drift out of sync with the two numbers it comes from.
	SeatsFree int `json:"seatsFree"`

	// DistanceMeters is populated only by the nearest-transport search.
	DistanceMeters float64 `json:"distanceMeters,omitempty"`
}

// Booking records one claim of N seats on a transport for an incident. It is the
// audit trail; the authoritative seat count lives on Transport.SeatsTaken, because
// only a running total can be checked atomically inside the capacity guard.
type Booking struct {
	ID          string    `json:"id"`
	TransportID string    `json:"transportId"`
	IncidentID  string    `json:"incidentId"`
	Seats       int       `json:"seats"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
