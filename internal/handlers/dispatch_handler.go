package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

type DispatchHandler struct {
	svc *service.DispatchService
}

func NewDispatchHandler(svc *service.DispatchService) *DispatchHandler {
	return &DispatchHandler{svc: svc}
}

// validUnitTypes is the set accepted for the ?type= preference. Empty ("") is also
// allowed and means "no preference".
var validUnitTypes = map[string]bool{
	models.UnitTypeAmbulance: true,
	models.UnitTypeFire:      true,
	models.UnitTypeRescue:    true,
	models.UnitTypePolice:    true,
}

// Candidates handles GET /incidents/:id/candidates?limit=&type= — the nearest
// available units to an incident (KNN shortlist), each scored and ranked best-first.
// The optional ?type= is the preferred unit type; a matching unit gets full
// type-match credit so it can outrank a closer wrong-type unit.
func (h *DispatchHandler) Candidates(c *gin.Context) {
	limit := clampInt(c.Query("limit"), 5, 1, 50)

	preferredType := c.Query("type")
	if preferredType != "" && !validUnitTypes[preferredType] {
		common.Error(c, http.StatusBadRequest, "invalid unit type", "VALIDATION_ERROR")
		return
	}

	inc, candidates, err := h.svc.Candidates(c.Request.Context(), c.Param("id"), preferredType, limit)
	if err != nil {
		if errors.Is(err, service.ErrIncidentNotFound) {
			common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch candidates", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "dispatch candidates", gin.H{
		"incident":   inc,
		"candidates": candidates,
	})
}

// dispatchRequest is the body of POST /incidents/:id/dispatch — which unit to send.
type dispatchRequest struct {
	UnitID string `json:"unitId" binding:"required"`
}

// Dispatch handles POST /incidents/:id/dispatch — reserve a chosen unit for the
// incident via the no-double-booking transaction. 201 with the dispatch record on
// success; 409 if the unit was already taken or the incident isn't dispatchable.
func (h *DispatchHandler) Dispatch(c *gin.Context) {
	var req dispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "unitId is required", "VALIDATION_ERROR")
		return
	}

	// ?strategy=pessimistic (default) | optimistic — the concurrency-control approach.
	strategy := service.StrategyPessimistic
	switch c.Query("strategy") {
	case "", string(service.StrategyPessimistic):
		strategy = service.StrategyPessimistic
	case string(service.StrategyOptimistic):
		strategy = service.StrategyOptimistic
	default:
		common.Error(c, http.StatusBadRequest, "strategy must be 'pessimistic' or 'optimistic'", "VALIDATION_ERROR")
		return
	}

	d, err := h.svc.Reserve(c.Request.Context(), c.Param("id"), req.UnitID, strategy)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrIncidentNotFound):
			common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
		case errors.Is(err, service.ErrUnitNotFound):
			common.Error(c, http.StatusNotFound, "unit not found", "NOT_FOUND")
		case errors.Is(err, service.ErrUnitUnavailable):
			common.Error(c, http.StatusConflict, "unit is no longer available", "CONFLICT")
		case errors.Is(err, service.ErrIncidentNotDispatchable):
			common.Error(c, http.StatusConflict, "incident cannot be dispatched", "CONFLICT")
		case errors.Is(err, service.ErrReservationConflict):
			common.Error(c, http.StatusConflict, "reservation conflicted, please retry", "CONFLICT")
		default:
			common.Error(c, http.StatusInternalServerError, "could not dispatch unit", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusCreated, "unit dispatched", d)
}

// ListByIncident handles GET /incidents/:id/dispatches — the incident's dispatches.
func (h *DispatchHandler) ListByIncident(c *gin.Context) {
	ds, err := h.svc.ListIncidentDispatches(c.Request.Context(), c.Param("id"))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list dispatches", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "incident dispatches", ds)
}

// Get handles GET /dispatches/:id — a single dispatch.
func (h *DispatchHandler) Get(c *gin.Context) {
	d, err := h.svc.GetDispatch(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrDispatchNotFound) {
			common.Error(c, http.StatusNotFound, "dispatch not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch dispatch", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "dispatch", d)
}

// advanceStatusRequest is the body of PATCH /dispatches/:id/status. reserved is
// omitted — it's only ever the initial state, never a transition target.
type advanceStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=en_route on_scene completed cancelled"`
}

// AdvanceStatus handles PATCH /dispatches/:id/status — move a dispatch along its
// lifecycle. The unit's status is synced and the incident auto-resolves when its
// last active dispatch completes (all handled atomically in the service/repo).
func (h *DispatchHandler) AdvanceStatus(c *gin.Context) {
	var req advanceStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "status must be one of en_route, on_scene, completed, cancelled", "VALIDATION_ERROR")
		return
	}

	d, err := h.svc.AdvanceStatus(c.Request.Context(), c.Param("id"), req.Status)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDispatchNotFound):
			common.Error(c, http.StatusNotFound, "dispatch not found", "NOT_FOUND")
		case errors.Is(err, service.ErrIllegalDispatchTransition):
			common.Error(c, http.StatusConflict, "illegal status transition", "CONFLICT")
		case errors.Is(err, service.ErrInvalidDispatchStatus):
			common.Error(c, http.StatusBadRequest, "invalid dispatch status", "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not update dispatch", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "dispatch updated", d)
}
