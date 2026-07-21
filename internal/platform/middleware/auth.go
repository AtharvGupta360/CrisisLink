package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/auth"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
)

// AuthRequired guards protected routes: it requires a valid "Authorization:
// Bearer <jwt>" header, verifies the token's signature + expiry, and puts the
// caller's identity on the context for downstream handlers. Any failure writes a
// 401 and aborts the chain — the handler never runs.
//
// RBAC layers on top: RequireRole, placed AFTER this one, reads the role set
// here; handlers then apply object-level ownership checks via ActorFrom.
func AuthRequired(jwtCfg *config.JWTConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			common.Error(c, http.StatusUnauthorized, "missing authorization header", "UNAUTHORIZED")
			c.Abort()
			return
		}

		// Expect exactly "Bearer <token>". SplitN with limit 2 keeps any spaces
		// inside the token intact (JWTs won't have them, but this is the correct
		// way to parse a scheme + credential).
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			common.Error(c, http.StatusUnauthorized, "invalid authorization format", "UNAUTHORIZED")
			c.Abort()
			return
		}

		claims, err := auth.ValidateToken(parts[1], jwtCfg)
		if err != nil {
			// Same generic message for tampered / expired / malformed — don't
			// hand an attacker diagnostic detail.
			common.Error(c, http.StatusUnauthorized, "invalid or expired token", "UNAUTHORIZED")
			c.Abort()
			return
		}

		// Identity available to every downstream handler via c.GetString(...).
		c.Set("userID", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		// Ownership bindings from the token, used by handlers for object-level
		// checks. Empty for roles that are not bound to a resource.
		c.Set("unitID", claims.UnitID)
		c.Set("shelterID", claims.ShelterID)
		c.Next()
	}
}
