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
