package models

import "time"

// Unit specializations (validated by the service + a DB CHECK constraint).
const (
	UnitTypeAmbulance = "ambulance"
	UnitTypeFire      = "fire"
	UnitTypeRescue    = "rescue"
	UnitTypePolice    = "police"
)

// Unit statuses. available is the only "dispatchable" state; the reservation
// transaction (P13) flips available -> reserved. Full lifecycle transitions come
// in P15.
const (
	UnitAvailable    = "available"
	UnitReserved     = "reserved"
	UnitEnRoute      = "en_route"
	UnitOnScene      = "on_scene"
	UnitOutOfService = "out_of_service"
)

// Unit is a rescue resource in the fleet. Like Incident, the position is stored
// as geometry(Point,4326) and carried in Go as Latitude/Longitude floats.
type Unit struct {
	ID        string    `json:"id"`
	CallSign  string    `json:"callSign"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
