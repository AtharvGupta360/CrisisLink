package transport

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

type TransportHandler struct {
	svc *TransportService
}

func NewTransportHandler(svc *TransportService) *TransportHandler {
	return &TransportHandler{svc: svc}
}

type createRequest struct {
	CallSign string `json:"callSign" binding:"required,min=2,max=32"`
	Kind     string `json:"kind" binding:"required,oneof=bus boat helicopter truck"`
	Capacity int    `json:"capacity" binding:"required,min=1"`
	// Coordinates are required but deliberately NOT tagged required: gin treats a
	// zero value as missing, and 0.0 is a valid coordinate. Range-checked in the service.
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (h *TransportHandler) Create(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}
	t, err := h.svc.Create(c.Request.Context(), CreateInput{
		CallSign: req.CallSign, Kind: req.Kind, Capacity: req.Capacity,
		Latitude: req.Latitude, Longitude: req.Longitude,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidKind), errors.Is(err, ErrInvalidCapacity), errors.Is(err, geo.ErrInvalidCoordinates):
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		case errors.Is(err, ErrDuplicateCallSign):
			common.Error(c, http.StatusConflict, "call sign already exists", "CONFLICT")
		default:
			common.Error(c, http.StatusInternalServerError, "could not create transport", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusCreated, "transport created", t)
}

func (h *TransportHandler) List(c *gin.Context) {
	ts, err := h.svc.List(c.Request.Context(), c.Query("status"), c.Query("kind"),
		common.ClampInt(c.Query("limit"), 20, 1, 100), common.ClampInt(c.Query("offset"), 0, 0, 100000))
	if err != nil {
		if errors.Is(err, ErrInvalidStatus) || errors.Is(err, ErrInvalidKind) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not list transports", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "transports", ts)
}

func (h *TransportHandler) GetByID(c *gin.Context) {
	t, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrTransportNotFound) {
			common.Error(c, http.StatusNotFound, "transport not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch transport", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "transport", t)
}

// Nearest handles GET /transports/nearest?lat=&lng=&seats=&limit= — vehicles that
// can seat the whole group, nearest first.
func (h *TransportHandler) Nearest(c *gin.Context) {
	lat, errLat := strconv.ParseFloat(c.Query("lat"), 64)
	lng, errLng := strconv.ParseFloat(c.Query("lng"), 64)
	if errLat != nil || errLng != nil {
		common.Error(c, http.StatusBadRequest, "lat and lng are required", "VALIDATION_ERROR")
		return
	}
	seats := common.ClampInt(c.Query("seats"), 1, 1, 500)
	ts, err := h.svc.FindNearest(c.Request.Context(), lat, lng, seats, common.ClampInt(c.Query("limit"), 5, 1, 50))
	if err != nil {
		if errors.Is(err, geo.ErrInvalidCoordinates) || errors.Is(err, ErrInvalidSeats) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not search transports", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "nearest transports with room", gin.H{"seats": seats, "transports": ts})
}

type bookRequest struct {
	TransportID string `json:"transportId" binding:"required,uuid"`
	Seats       int    `json:"seats" binding:"required,min=1"`
}

// Book handles POST /incidents/:id/transport-bookings — claim N seats atomically.
func (h *TransportHandler) Book(c *gin.Context) {
	var req bookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "transportId and seats are required", "VALIDATION_ERROR")
		return
	}
	b, t, err := h.svc.Book(c.Request.Context(), req.TransportID, c.Param("id"), req.Seats)
	if err != nil {
		switch {
		case errors.Is(err, ErrTransportNotFound):
			common.Error(c, http.StatusNotFound, "transport not found", "NOT_FOUND")
		case errors.Is(err, ErrIncidentNotFound):
			common.Error(c, http.StatusNotFound, "incident not found", "NOT_FOUND")
		case errors.Is(err, ErrTransportUnavailable):
			common.Error(c, http.StatusConflict, "transport is not available", "CONFLICT")
		case errors.Is(err, ErrInsufficientSeats):
			// 409, not 400: the request was valid, the resource simply cannot satisfy
			// it right now — and it might succeed later if a booking is cancelled.
			common.Error(c, http.StatusConflict, "not enough free seats", "CONFLICT")
		case errors.Is(err, ErrInvalidSeats):
			common.Error(c, http.StatusBadRequest, "seats must be positive", "VALIDATION_ERROR")
		default:
			common.Error(c, http.StatusInternalServerError, "could not book seats", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusCreated, "seats booked", gin.H{"booking": b, "transport": t})
}

// Cancel handles PATCH /transport-bookings/:id/cancel — release the seats.
func (h *TransportHandler) Cancel(c *gin.Context) {
	b, t, err := h.svc.Cancel(c.Request.Context(), c.Param("id"))
	if err != nil {
		switch {
		case errors.Is(err, ErrBookingNotFound):
			common.Error(c, http.StatusNotFound, "booking not found", "NOT_FOUND")
		case errors.Is(err, ErrBookingNotActive):
			common.Error(c, http.StatusConflict, "booking is not active", "CONFLICT")
		default:
			common.Error(c, http.StatusInternalServerError, "could not cancel booking", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "booking cancelled, seats released", gin.H{"booking": b, "transport": t})
}

// ListBookings handles GET /incidents/:id/transport-bookings.
func (h *TransportHandler) ListBookings(c *gin.Context) {
	bs, err := h.svc.ListBookings(c.Request.Context(), c.Param("id"), common.ClampInt(c.Query("limit"), 20, 1, 100))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list bookings", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "transport bookings", bs)
}
