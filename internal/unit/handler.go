package unit

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

type UnitHandler struct {
	svc *UnitService
}

func NewUnitHandler(svc *UnitService) *UnitHandler {
	return &UnitHandler{svc: svc}
}

type CreateUnitRequest struct {
	CallSign  string  `json:"callSign" binding:"required,min=2,max=50"`
	Type      string  `json:"type" binding:"required,oneof=ambulance fire rescue police"`
	Latitude  float64 `json:"latitude" binding:"min=-90,max=90"`
	Longitude float64 `json:"longitude" binding:"min=-180,max=180"`
}

func (h *UnitHandler) Create(c *gin.Context) {
	var req CreateUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	u, err := h.svc.Create(c.Request.Context(), CreateUnitInput{
		CallSign:  req.CallSign,
		Type:      req.Type,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrDuplicateCallSign):
			common.Error(c, http.StatusConflict, "call sign already exists", "DUPLICATE_CALL_SIGN")
		case errors.Is(err, ErrInvalidUnitType) || errors.Is(err, geo.ErrInvalidCoordinates):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not create unit", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusCreated, "unit registered", u)
}

func (h *UnitHandler) List(c *gin.Context) {
	units, err := h.svc.List(c.Request.Context(),
		c.Query("status"), c.Query("type"),
		common.ClampInt(c.Query("limit"), 50, 1, 200),
		common.ClampInt(c.Query("offset"), 0, 0, 1_000_000),
	)
	if err != nil {
		if errors.Is(err, ErrInvalidUnitStatus) || errors.Is(err, ErrInvalidUnitType) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not list units", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "units", units)
}

func (h *UnitHandler) GetByID(c *gin.Context) {
	u, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrUnitNotFound) {
			common.Error(c, http.StatusNotFound, "unit not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch unit", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "unit", u)
}

type UpdateUnitStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=available reserved en_route on_scene out_of_service"`
}

func (h *UnitHandler) UpdateStatus(c *gin.Context) {
	var req UpdateUnitStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	u, err := h.svc.UpdateStatus(c.Request.Context(), c.Param("id"), req.Status)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnitNotFound):
			common.Error(c, http.StatusNotFound, "unit not found", "NOT_FOUND")
		case errors.Is(err, ErrInvalidUnitStatus):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not update unit", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "unit status updated", u)
}
