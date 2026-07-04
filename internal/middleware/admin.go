package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
)

// AdminRequired allows only users whose role is "admin". It MUST run AFTER
// AuthRequired, which is what puts "role" on the context — order in the chain
// matters. Returns 403 Forbidden (not 401): the caller IS authenticated, they
// just aren't authorized for this action.
//
// (If we later need more roles, generalize this to RequireRole(roles ...string);
// AdminRequired is the common case spelled out.)
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != "admin" {
			common.Error(c, http.StatusForbidden, "admin access required", "FORBIDDEN")
			c.Abort()
			return
		}
		c.Next()
	}
}
