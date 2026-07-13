package models

import "time"

// Victim lifecycle statuses. registered at intake (P17); sheltered once assigned
// to a shelter (P18); discharged when they leave.
const (
	VictimRegistered = "registered"
	VictimSheltered  = "sheltered"
	VictimDischarged = "discharged"
)

// Victim is a person needing shelter. ShelterID is a pointer because it is NULL
// until the victim is assigned to a shelter — "unassigned" is absence, not a value.
type Victim struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Notes     string    `json:"notes"`
	ShelterID *string   `json:"shelterId"` // nil until assigned (P18)
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
