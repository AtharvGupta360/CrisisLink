package dispatch

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// reassignError maps the shared failure modes of reroute and preempt.
func reassignError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrDispatchNotFound):
		common.Error(c, http.StatusNotFound, "dispatch not found", "NOT_FOUND")
	case errors.Is(err, incident.ErrIncidentNotFound):
		common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
	case errors.Is(err, ErrIllegalDispatchTransition):
		common.Error(c, http.StatusConflict, "dispatch is no longer active", "CONFLICT")
	case errors.Is(err, ErrIncidentNotDispatchable):
		common.Error(c, http.StatusConflict, "incident cannot be dispatched", "CONFLICT")
	case errors.Is(err, ErrNoReassignCandidate):
		// 409, not 404: the request was valid and might succeed later when a unit
		// frees up. A 404 would suggest the incident itself does not exist.
		common.Error(c, http.StatusConflict, "no suitable unit available to take over", "CONFLICT")
	default:
		common.Error(c, http.StatusInternalServerError, "could not reassign dispatch", "INTERNAL_ERROR")
	}
}

// Reroute handles POST /dispatches/:id/reroute — the assigned unit can no longer
// respond, so hand the incident to the next-best available unit.
func (h *DispatchHandler) Reroute(c *gin.Context) {
	d, err := h.svc.Reroute(c.Request.Context(), c.Param("id"))
	if err != nil {
		reassignError(c, err)
		return
	}
	common.Success(c, http.StatusCreated, "dispatch rerouted to a new unit", d)
}

// Preempt handles POST /incidents/:id/preempt — take a unit from a LESS SEVERE
// incident for this one. The escalation of last resort when nothing is free.
func (h *DispatchHandler) Preempt(c *gin.Context) {
	d, err := h.svc.Preempt(c.Request.Context(), c.Param("id"))
	if err != nil {
		reassignError(c, err)
		return
	}
	common.Success(c, http.StatusCreated, "unit preempted from a lower-severity incident", d)
}

// Preemptable handles GET /incidents/:id/preemptable — what COULD be taken, and at
// whose expense. Preemption harms someone, so the cost should be visible before an
// operator commits to it.
func (h *DispatchHandler) Preemptable(c *gin.Context) {
	inc, victims, err := h.svc.PreemptableFor(c.Request.Context(), c.Param("id"))
	if err != nil {
		reassignError(c, err)
		return
	}
	common.Success(c, http.StatusOK, "units that could be preempted", gin.H{
		"incident":    inc,
		"preemptable": victims,
	})
}
