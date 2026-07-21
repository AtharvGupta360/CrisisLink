package auth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

type AuthHandler struct {
	authService *Service
}

func NewAuthHandler(authService *Service) *AuthHandler {
	return &AuthHandler{authService: authService}
}

// RegisterRequest is the expected JSON body. The `binding` tags are validated by
// gin (go-playground/validator) before our code runs — bad input never reaches
// the service.
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	user, token, err := h.authService.Register(c.Request.Context(), req.Username, req.Email, req.Password)
	if err != nil {
		if errors.Is(err, ErrDuplicateUser) {
			common.Error(c, http.StatusConflict, "username or email already exists", "DUPLICATE_USER")
			return
		}
		common.Error(c, http.StatusInternalServerError, "could not register user", "INTERNAL_ERROR")
		return
	}

	// user serializes without the password (json:"-"); token is the JWT.
	common.Success(c, http.StatusCreated, "user registered successfully", gin.H{
		"user":  user,
		"token": token,
	})
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, err.Error(), "VALIDATION_ERROR")
		return
	}

	user, token, err := h.authService.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		// Always the same 401 — don't distinguish "no such user" from "bad password".
		common.Error(c, http.StatusUnauthorized, "invalid credentials", "INVALID_CREDENTIALS")
		return
	}

	common.Success(c, http.StatusOK, "login successful", gin.H{
		"user":  user,
		"token": token,
	})
}

// AssignRoleRequest is the admin's role change. UnitID/ShelterID are pointers so
// "absent" is distinguishable from "empty string".
type AssignRoleRequest struct {
	Role      string  `json:"role" binding:"required"`
	UnitID    *string `json:"unitId"`
	ShelterID *string `json:"shelterId"`
}

// AssignRole handles PATCH /admin/users/:id/role — the only path by which a
// privileged role is ever granted. Registration always produces a citizen, so
// there is no self-service escalation.
func (h *AuthHandler) AssignRole(c *gin.Context) {
	var req AssignRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.Error(c, http.StatusBadRequest, "role is required", "VALIDATION_ERROR")
		return
	}

	u, err := h.authService.AssignRole(c.Request.Context(), c.Param("id"), req.Role, req.UnitID, req.ShelterID)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidRole):
			common.Error(c, http.StatusBadRequest, "invalid role", "VALIDATION_ERROR")
		case errors.Is(err, ErrBindingMissing):
			common.Error(c, http.StatusBadRequest,
				"responder requires unitId; shelter_manager requires shelterId", "VALIDATION_ERROR")
		case errors.Is(err, ErrUserNotFound):
			common.Error(c, http.StatusNotFound, "user not found", "NOT_FOUND")
		default:
			common.Error(c, http.StatusInternalServerError, "could not assign role", "INTERNAL_ERROR")
		}
		return
	}
	// The user must obtain a NEW token for this to take effect: the old one still
	// carries the previous role and bindings until it expires.
	common.Success(c, http.StatusOK, "role assigned; user must re-login for it to take effect", u)
}
