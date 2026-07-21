package presence

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/middleware"
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

	// OBJECT-LEVEL AUTHORIZATION. The role gate only established that the caller is
	// a responder; this establishes that it is THEIR unit. Without it any responder
	// could spoof any other unit's position — role-checked and still broken.
	// Operators and admins are unbound and may report for any unit.
	if !middleware.ActorFrom(c).OwnsUnit(unitID) {
		common.Error(c, http.StatusForbidden, "you may only send heartbeats for your own unit", "FORBIDDEN")
		return
	}

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

// NearbyLive handles GET /units/nearby?lat=&lng=&radius=&limit= — which units are
// physically near this point RIGHT NOW, by live heartbeat position.
//
// This is the question PostGIS cannot answer well: its units.location is the
// registration pin, so a unit that has driven across the city still appears where
// it started. Here the positions are seconds old.
func (h *Handler) NearbyLive(c *gin.Context) {
	lat, errLat := strconv.ParseFloat(c.Query("lat"), 64)
	lng, errLng := strconv.ParseFloat(c.Query("lng"), 64)
	if errLat != nil || errLng != nil {
		common.Error(c, http.StatusBadRequest, "lat and lng are required", "VALIDATION_ERROR")
		return
	}
	radius := float64(common.ClampInt(c.Query("radius"), 5000, 1, 100000))
	limit := common.ClampInt(c.Query("limit"), 5, 1, 50)

	units, err := h.svc.NearbyLive(c.Request.Context(), lat, lng, radius, limit)
	if err != nil {
		if errors.Is(err, geo.ErrInvalidCoordinates) {
			common.Error(c, http.StatusBadRequest, "coordinates out of range", "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusServiceUnavailable, "presence store unavailable", "PRESENCE_UNAVAILABLE")
		return
	}
	common.Success(c, http.StatusOK, "live units nearby", gin.H{
		"count": len(units),
		"units": units,
	})
}
