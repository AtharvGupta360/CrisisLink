package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
)

// Recovery turns a panic in any handler into a logged 500 + clean JSON envelope
// instead of crashing the process. In a monolith one bad handler must not take
// the whole binary down — this is that mitigation.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				common.Logger.Errorw("panic recovered",
					"error", err,
					"request_id", c.GetString(RequestIDHeader),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
				)
				common.Error(c, http.StatusInternalServerError, "internal server error", "INTERNAL_ERROR")
				c.Abort()
			}
		}()
		c.Next()
	}
}
