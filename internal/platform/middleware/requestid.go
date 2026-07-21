// Package middleware holds the Gin middleware chain: request-id, recovery,
// logging (here), plus CORS/auth/RBAC/rate-limit added in later phases.
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDHeader is read from the incoming request (to reuse a caller/gateway
// id) and echoed on the response, so one request is traceable across all logs.
const RequestIDHeader = "X-Request-ID"

// RequestID resolves a correlation id per request: reuse the inbound
// X-Request-ID if present, else mint a UUID. Stored on the context (for the
// logger and handlers) and echoed on the response.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(RequestIDHeader, id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}
