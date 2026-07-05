package models

import "time"

// Dispatch lifecycle statuses. reserved is the initial state written by the P13
// reservation transaction; the en_route -> on_scene -> completed progression (and
// cancelled) is the dispatch lifecycle built in P15. The first three are the
// "active" states that count against a unit's one-active-dispatch rule.
const (
	DispatchReserved  = "reserved"
	DispatchEnRoute   = "en_route"
	DispatchOnScene   = "on_scene"
	DispatchCompleted = "completed"
	DispatchCancelled = "cancelled"
)

// Dispatch is the assignment of one unit to one incident. It is created
// atomically with flipping the unit to 'reserved' (see the reservation
// transaction), so a dispatch row always corresponds to a held unit.
type Dispatch struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incidentId"`
	UnitID     string    `json:"unitId"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
