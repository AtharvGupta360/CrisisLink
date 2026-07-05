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
