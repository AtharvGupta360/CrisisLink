package victim

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/authz"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/middleware"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
)

type VictimHandler struct {
	svc *VictimService
}

func NewVictimHandler(svc *VictimService) *VictimHandler {
	return &VictimHandler{svc: svc}
}

type CreateVictimRequest struct {
	Name      string  `json:"name" binding:"required,min=1,max=100"`
	Notes     string  `json:"notes" binding:"max=2000"`
	Latitude  float64 `json:"latitude" binding:"min=-90,max=90"`
	Longitude float64 `json:"longitude" binding:"min=-180,max=180"`
}

func (h *VictimHandler) Create(c *gin.Context) {
	var req CreateVictimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	v, err := h.svc.Create(c.Request.Context(), CreateVictimInput{
		Name:      req.Name,
		Notes:     req.Notes,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
	})
	if err != nil {
		if errors.Is(err, geo.ErrInvalidCoordinates) {
			common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not register victim", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusCreated, "victim registered", v)
}

func (h *VictimHandler) List(c *gin.Context) {
	// A shelter manager sees ONLY the victims in their own shelter. The scope comes
	// from the verified token, never from a query parameter — otherwise a manager
	// could simply ask for someone else's shelter and the restriction would be
	// decorative. Operators and admins pass an empty scope and see everything.
	scope := ""
	if actor := middleware.ActorFrom(c); actor.Is(authz.RoleShelterManager) {
		scope = actor.ShelterID
	}

	victims, err := h.svc.List(c.Request.Context(),
		c.Query("status"), scope,
		common.ClampInt(c.Query("limit"), 50, 1, 200),
		common.ClampInt(c.Query("offset"), 0, 0, 1_000_000),
	)
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list victims", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "victims", victims)
}

func (h *VictimHandler) GetByID(c *gin.Context) {
	v, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrVictimNotFound) {
			common.Error(c, http.StatusNotFound, "victim not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch victim", "INTERNAL_ERROR")
		return
	}
	// Object-level check, applied AFTER the lookup because ownership lives on the
	// record: a shelter manager may only read a victim housed in their own shelter.
	// Returning 404 rather than 403 here is deliberate — a 403 would confirm the
	// record exists, which is itself a disclosure when the id is being enumerated.
	if actor := middleware.ActorFrom(c); actor.Is(authz.RoleShelterManager) {
		if v.ShelterID == nil || *v.ShelterID != actor.ShelterID {
			common.Error(c, http.StatusNotFound, "victim not found", "NOT_FOUND")
			return
		}
	}

	common.Success(c, http.StatusOK, "victim", v)
}

// NearestShelters handles GET /victims/:id/shelters?limit= — the nearest open
// shelters with room for this victim (KNN). P18 will assign the victim to one.
func (h *VictimHandler) NearestShelters(c *gin.Context) {
	limit := common.ClampInt(c.Query("limit"), 5, 1, 50)

	v, shelters, err := h.svc.NearestShelters(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		if errors.Is(err, ErrVictimNotFound) {
			common.Error(c, http.StatusNotFound, "victim not found", "NOT_FOUND")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not fetch shelters", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "ranked open shelters", gin.H{
		"victim":   v,
		"shelters": shelters,
	})
}

type AssignVictimRequest struct {
	ShelterID string `json:"shelterId" binding:"required"`
}

// Assign handles POST /victims/:id/assign — place the victim into a chosen shelter
// (no-overflow transaction). 200 with the updated victim + shelter; 409 if the
// victim is already assigned or the shelter is full/closed.
func (h *VictimHandler) Assign(c *gin.Context) {
	var req AssignVictimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "shelterId is required", "VALIDATION_ERROR")
		return
	}

	v, sh, err := h.svc.Assign(c.Request.Context(), c.Param("id"), req.ShelterID)
	if err != nil {
		switch {
		case errors.Is(err, ErrVictimNotFound):
			common.Error(c, http.StatusNotFound, "victim not found", "NOT_FOUND")
		case errors.Is(err, shelter.ErrShelterNotFound):
			common.Error(c, http.StatusNotFound, "shelter not found", "NOT_FOUND")
		case errors.Is(err, ErrVictimAlreadyAssigned):
			common.Error(c, http.StatusConflict, "victim is already assigned to a shelter", "CONFLICT")
		case errors.Is(err, shelter.ErrShelterClosed):
			common.Error(c, http.StatusConflict, "shelter is closed", "CONFLICT")
		case errors.Is(err, shelter.ErrShelterFull):
			common.Error(c, http.StatusConflict, "shelter is full", "CONFLICT")
		default:
			common.Error(c, http.StatusInternalServerError, "could not assign victim", "INTERNAL_ERROR")
		}
		return
	}
	common.Success(c, http.StatusOK, "victim assigned to shelter", gin.H{
		"victim":  v,
		"shelter": sh,
	})
}
