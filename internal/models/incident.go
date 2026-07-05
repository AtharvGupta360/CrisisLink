package models

import "time"

// Incident severity levels (validated by the service + a DB CHECK constraint).
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Incident lifecycle statuses. See the state machine in the incident service.
const (
	StatusReported   = "reported"
	StatusVerified   = "verified"
	StatusDispatched = "dispatched"
	StatusResolved   = "resolved"
	StatusCancelled  = "cancelled"
)

// Incident is a geolocated, citizen-reported event. The DB stores the position as
// a geometry(Point,4326); in Go we carry it as plain Latitude/Longitude floats,
// converting at the SQL boundary (ST_MakePoint on write, ST_X/ST_Y on read).
type Incident struct {
	ID          string    `json:"id"`
	ReporterID  string    `json:"reporterId"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"`
	Status      string    `json:"status"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
