// Package httpx holds the shared HTTP layer built on Gin: engine construction and
// the base middleware chain every route gets (request-id -> recovery -> logging).
// It depends on no internal module, so all feature packages can build on it
// without creating import cycles. P5 formalizes/extends this chain (adding
// metrics); this is the production-grade baseline.
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDHeader is the canonical header we read an incoming correlation id from
// and echo back on the response, so a single request can be traced end-to-end
// across logs (and, later, across services).
const RequestIDHeader = "X-Request-ID"

// ctxRequestID is the key under which the resolved request id is stored on the
// gin.Context for handlers/loggers to read.
const ctxRequestID = "request_id"

// NewEngine builds a *gin.Engine with the base middleware chain. We use gin.New()
// (a bare engine) rather than gin.Default() so we OWN the middleware explicitly —
// Default installs a stdout logger and recovery that are not production-shaped.
func NewEngine(logger *slog.Logger, production bool) *gin.Engine {
	if production {
		// Release mode: disables debug logging and the startup warnings, and is
		// the mode you must run in prod (Gin itself warns otherwise).
		gin.SetMode(gin.ReleaseMode)
	}

	e := gin.New()
	// Order matters: request-id first (so recovery/logging can reference it),
	// recovery next (so a panic in logging or a handler is caught), then logging.
	e.Use(requestID(), recovery(logger), requestLogger(logger))
	return e
}

// RequestIDFrom returns the request id resolved for this request (for handlers
// that want to include it in a response or a downstream call).
func RequestIDFrom(c *gin.Context) string { return c.GetString(ctxRequestID) }

// requestID resolves a correlation id per request: reuse the caller's
// X-Request-ID if present (so a gateway/frontend id flows through), else mint a
// new UUID. Stored on the context and echoed on the response.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ctxRequestID, id)
		c.Writer.Header().Set(RequestIDHeader, id)
		c.Next()
	}
}

// recovery turns a panic in any handler into a logged 500 + clean JSON, instead
// of crashing the process. In a modular monolith one bad handler must not take
// the whole binary down (see the "blast radius" tradeoff in the architecture
// notes) — this is that mitigation.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {
		logger.Error("panic recovered",
			"err", err,
			"request_id", RequestIDFrom(c),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error":      "internal server error",
			"request_id": RequestIDFrom(c),
		})
	})
}

// requestLogger emits one structured log line per request after it completes,
// with the fields an on-call engineer actually needs to debug: method, path,
// status, latency, correlation id, client ip.
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // run the rest of the chain + handler first
		logger.Info("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFrom(c),
			"client_ip", c.ClientIP(),
		)
	}
}
