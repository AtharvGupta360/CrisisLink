package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

type DispatchHandler struct {
	svc *service.DispatchService
}

func NewDispatchHandler(svc *service.DispatchService) *DispatchHandler {
	return &DispatchHandler{svc: svc}
}

// Candidates handles GET /incidents/:id/candidates?limit= — the nearest available
// units to an incident (KNN). P12 will add scores to these.
func (h *DispatchHandler) Candidates(c *gin.Context) {
	limit := clampInt(c.Query("limit"), 5, 1, 50)

	inc, units, err := h.svc.Candidates(c.Request.Context(), c.Param("id"), limit)
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
		"candidates": units,
	})
}
