package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

type ShelterHandler struct {
	svc *service.ShelterService
}

func NewShelterHandler(svc *service.ShelterService) *ShelterHandler {
	return &ShelterHandler{svc: svc}
}

type CreateShelterRequest struct {
	Name      string  `json:"name" binding:"required,min=2,max=100"`
	Capacity  int     `json:"capacity" binding:"required,min=1"`
	Latitude  float64 `json:"latitude" binding:"min=-90,max=90"`
	Longitude float64 `json:"longitude" binding:"min=-180,max=180"`
}

func (h *ShelterHandler) Create(c *gin.Context) {
	var req CreateShelterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	sh, err := h.svc.Create(c.Request.Context(), service.CreateShelterInput{
		Name:      req.Name,
		Capacity:  req.Capacity,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDuplicateShelterName):
			common.Error(c, http.StatusConflict, "shelter name already exists", "DUPLICATE_NAME")
		case errors.Is(err, service.ErrInvalidCapacity) || errors.Is(err, service.ErrInvalidCoordinates):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not create shelter", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusCreated, "shelter registered", sh)
}

func (h *ShelterHandler) List(c *gin.Context) {
	shelters, err := h.svc.List(c.Request.Context(),
		c.Query("status"),
		clampInt(c.Query("limit"), 50, 1, 200),
		clampInt(c.Query("offset"), 0, 0, 1_000_000),
	)
	if err != nil {
		if errors.Is(err, service.ErrInvalidShelterStatus) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not list shelters", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "shelters", shelters)
}

func (h *ShelterHandler) GetByID(c *gin.Context) {
	sh, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrShelterNotFound) {
			common.Error(c, http.StatusNotFound, "shelter not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch shelter", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "shelter", sh)
}

type UpdateShelterStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=open closed"`
}

func (h *ShelterHandler) UpdateStatus(c *gin.Context) {
	var req UpdateShelterStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	sh, err := h.svc.UpdateStatus(c.Request.Context(), c.Param("id"), req.Status)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrShelterNotFound):
			common.Error(c, http.StatusNotFound, "shelter not found", "NOT_FOUND")
		case errors.Is(err, service.ErrInvalidShelterStatus):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not update shelter", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "shelter status updated", sh)
}
