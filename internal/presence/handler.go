package presence

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HeartbeatRequest is the unit reporting in. Coordinates are required but NOT
// tagged `binding:"required"` — gin treats a zero value as missing, and 0.0 is a
// legitimate latitude (the equator). Range validation happens in the service.
type HeartbeatRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Heartbeat handles POST /units/:id/heartbeat — "I am alive, and I am here."
//
// This is the highest-frequency endpoint in the system (every unit, every ~10s), so
// it does exactly one Redis SET and nothing else. No database read, no join, no
// validation against the units table.
func (h *Handler) Heartbeat(c *gin.Context) {
	var req HeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "latitude and longitude are required", "VALIDATION_ERROR")
		return
	}

	unitID := c.Param("id")
	if err := h.svc.Heartbeat(c.Request.Context(), unitID, req.Latitude, req.Longitude); err != nil {
		if errors.Is(err, geo.ErrInvalidCoordinates) {
			common.Error(c, http.StatusBadRequest, "coordinates out of range", "VALIDATION_ERROR")
			return
		}
		// A failed heartbeat is reported honestly rather than swallowed: the unit's
		// client needs to know it must retry, otherwise it will silently go dark
		// while believing it is checked in.
		common.Error(c, http.StatusServiceUnavailable, "could not record heartbeat", "PRESENCE_UNAVAILABLE")
		return
	}

	common.Success(c, http.StatusOK, "heartbeat recorded", gin.H{
		"unitId": unitID,
		// Tell the client the contract instead of hardcoding it on their side: the
		// server owns what "alive" means, and can change the cadence without a
		// client release.
		"nextHeartbeatWithinSeconds": HeartbeatInterval.Seconds(),
		"expiresInSeconds":           TTL.Seconds(),
	})
}

// GetPresence handles GET /units/:id/presence — is this unit alive, where was it
// last seen, and how stale is that?
//
// A unit that has gone dark is a 404: the presence record genuinely does not exist,
// because Redis deleted it when the heartbeats stopped. That is the whole design —
// nothing wrote "offline" anywhere.
func (h *Handler) GetPresence(c *gin.Context) {
	unitID := c.Param("id")
	p, found, err := h.svc.Get(c.Request.Context(), unitID)
	if err != nil {
		common.Error(c, http.StatusServiceUnavailable, "presence store unavailable", "PRESENCE_UNAVAILABLE")
		return
	}
	if !found {
		common.Error(c, http.StatusNotFound, "unit has gone dark (no heartbeat within TTL)", "NOT_PRESENT")
		return
	}
	common.Success(c, http.StatusOK, "unit is present", p)
}
