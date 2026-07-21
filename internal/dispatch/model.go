package dispatch

import (
	"time"

	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

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

// allowedDispatchTransitions is the dispatch lifecycle state machine (P15). A
// dispatch moves forward through the active phases or is cancelled; completed and
// cancelled are terminal. reserved is only ever the initial state, never a target.
var allowedDispatchTransitions = map[string][]string{
	DispatchReserved:  {DispatchEnRoute, DispatchCancelled},
	DispatchEnRoute:   {DispatchOnScene, DispatchCancelled},
	DispatchOnScene:   {DispatchCompleted, DispatchCancelled},
	DispatchCompleted: {},
	DispatchCancelled: {},
}

// IsValidDispatchStatus reports whether s is one of the known dispatch statuses.
func IsValidDispatchStatus(s string) bool {
	switch s {
	case DispatchReserved, DispatchEnRoute, DispatchOnScene, DispatchCompleted, DispatchCancelled:
		return true
	}
	return false
}

// CanTransitionDispatch reports whether from -> to is a legal lifecycle move.
func CanTransitionDispatch(from, to string) bool {
	for _, s := range allowedDispatchTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// IsActiveDispatch reports whether a status counts as the dispatch still holding
// its unit (matches the partial unique index's active set).
func IsActiveDispatch(status string) bool {
	return status == DispatchReserved || status == DispatchEnRoute || status == DispatchOnScene
}

// UnitStatusForDispatch maps a dispatch status to the unit status it implies:
// active phases mirror onto the unit; terminal phases free it back to available.
func UnitStatusForDispatch(dispatchStatus string) string {
	switch dispatchStatus {
	case DispatchReserved:
		return unit.UnitReserved
	case DispatchEnRoute:
		return unit.UnitEnRoute
	case DispatchOnScene:
		return unit.UnitOnScene
	default: // completed, cancelled
		return unit.UnitAvailable
	}
}
