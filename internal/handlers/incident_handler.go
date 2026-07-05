package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

type IncidentHandler struct {
	svc *service.IncidentService
}

func NewIncidentHandler(svc *service.IncidentService) *IncidentHandler {
	return &IncidentHandler{svc: svc}
}

// CreateIncidentRequest is the report body. NOTE: latitude/longitude use min/max
// but NOT `required` — gin treats a numeric 0 as "missing", and 0 is a VALID
// coordinate (equator / prime meridian). The service re-validates the range.
type CreateIncidentRequest struct {
	Title       string  `json:"title" binding:"required,min=3,max=200"`
	Description string  `json:"description" binding:"max=2000"`
	Severity    string  `json:"severity" binding:"required,oneof=low medium high critical"`
	Latitude    float64 `json:"latitude" binding:"min=-90,max=90"`
	Longitude   float64 `json:"longitude" binding:"min=-180,max=180"`
}

func (h *IncidentHandler) Create(c *gin.Context) {
	var req CreateIncidentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	// The reporter is the authenticated caller — taken from the token, never the
	// request body (a client can't report "as" someone else).
	inc, err := h.svc.Create(c.Request.Context(), service.CreateIncidentInput{
		ReporterID:  c.GetString("userID"),
		Title:       req.Title,
		Description: req.Description,
		Severity:    req.Severity,
		Latitude:    req.Latitude,
		Longitude:   req.Longitude,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidSeverity) || errors.Is(err, service.ErrInvalidCoordinates) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not create incident", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusCreated, "incident reported", inc)
}

func (h *IncidentHandler) GetByID(c *gin.Context) {
	inc, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrIncidentNotFound) {
			common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch incident", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "incident", inc)
}

func (h *IncidentHandler) List(c *gin.Context) {
	limit := clampInt(c.Query("limit"), 20, 1, 100)
	offset := clampInt(c.Query("offset"), 0, 0, 1_000_000)

	incidents, err := h.svc.List(c.Request.Context(), limit, offset)
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list incidents", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "incidents", incidents)
}

type UpdateStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=reported verified dispatched resolved cancelled"`
}

func (h *IncidentHandler) UpdateStatus(c *gin.Context) {
	var req UpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	inc, err := h.svc.UpdateStatus(c.Request.Context(), c.Param("id"), req.Status)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrIncidentNotFound):
			common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
		case errors.Is(err, service.ErrIllegalTransition):
			// 409 Conflict: the request is valid but conflicts with current state.
			common.Error(c, http.StatusConflict, err.Error(), "ILLEGAL_TRANSITION")
		case errors.Is(err, service.ErrInvalidStatus):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not update status", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "status updated", inc)
}

// clampInt parses a query int with a default and inclusive [min,max] bounds.
func clampInt(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
