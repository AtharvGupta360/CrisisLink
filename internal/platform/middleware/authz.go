package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/authz"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// RequireRole allows only callers holding one of the listed roles. It MUST run
// AFTER AuthRequired, which is what puts the identity on the context — order in
// the chain is load-bearing.
//
// It returns 403 Forbidden, not 401 Unauthorized: the caller IS authenticated,
// they simply are not permitted this action. Conflating the two tells an attacker
// the wrong thing and confuses legitimate clients about whether to re-login.
//
// IMPORTANT — WHAT THIS DOES NOT DO. A role gate answers "what KIND of user are
// you", never "is this YOUR resource". A responder passing RequireRole(Responder)
// could still act on someone else's unit. Object-level ownership is enforced
// separately, in the handlers, via authz.Actor.OwnsUnit / OwnsShelter. Both layers
// are needed; this one alone is the classic OWASP A01 mistake.
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !ActorFrom(c).Is(roles...) {
			common.Error(c, http.StatusForbidden, "insufficient role for this action", "FORBIDDEN")
			c.Abort()
			return
		}
		c.Next()
	}
}

// ActorFrom builds the caller's authorization identity from the context values
// that AuthRequired set out of the VERIFIED token.
//
// It deliberately reads only context values, never request input: a caller must
// never be able to influence who the server thinks they are by sending a header or
// body field.
func ActorFrom(c *gin.Context) authz.Actor {
	return authz.Actor{
		UserID:    c.GetString("userID"),
		Username:  c.GetString("username"),
		Role:      c.GetString("role"),
		UnitID:    c.GetString("unitID"),
		ShelterID: c.GetString("shelterID"),
	}
}
