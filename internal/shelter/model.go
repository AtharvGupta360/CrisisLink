package shelter

import "time"

// Shelter statuses. open accepts new intake; closed does not (admin override).
// "full" is NOT a stored status — fullness is derived (occupancy >= capacity), so
// it can never drift out of sync with the actual counts.
const (
	ShelterOpen   = "open"
	ShelterClosed = "closed"
)

// Shelter is a geolocated refuge with finite capacity. Unlike a Unit (binary
// available/not), a shelter is a counter: occupancy rises toward capacity.
type Shelter struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Capacity  int       `json:"capacity"`
	Occupancy int       `json:"occupancy"`
	Status    string    `json:"status"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// AvailableSpots is derived (capacity - occupancy), populated on read — never
	// stored, so it can't disagree with capacity/occupancy.
	AvailableSpots int `json:"availableSpots"`

	// DistanceMeters is populated only by the nearest-shelter search (P17+);
	// omitted from JSON on other reads.
	DistanceMeters float64 `json:"distanceMeters,omitempty"`
}
