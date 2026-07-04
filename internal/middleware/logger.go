package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
)

// RequestLogger emits one structured log line per request after it completes,
// with the fields an on-call engineer needs: method, path, status, latency,
// correlation id, client ip.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // run the rest of the chain + handler first
		common.Logger.Infow("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString(RequestIDHeader),
			"client_ip", c.ClientIP(),
		)
	}
}
