package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
)

// CORS tells browsers which origins may call this API. It is a BROWSER mechanism
// (curl/other backends ignore it) — not an auth control. We echo back the
// specific allowed origin (never "*" alongside credentials) and reject unknown
// origins with 403.
//
// The origin lookup map + joined header strings are built ONCE at startup
// (outside the returned closure), so every request just does map lookups.
func CORS(cfg *config.CORSConfig) gin.HandlerFunc {
	allowOriginMap := make(map[string]bool, len(cfg.AllowedOrigins))
	allowAll := false
	for _, origin := range cfg.AllowedOrigins {
		if origin == "*" {
			allowAll = true
			break
		}
		allowOriginMap[strings.ToLower(origin)] = true
	}
	methodStr := strings.Join(cfg.AllowedMethods, ",")
	headerStr := strings.Join(cfg.AllowedHeaders, ",")
	exposeStr := strings.Join(cfg.ExposedHeaders, ",")

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next() // not a browser cross-origin request; nothing to do
			return
		}

		if !(allowAll || allowOriginMap[strings.ToLower(origin)]) {
			common.Logger.Warnf("CORS: rejected origin %s", origin)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		// Headers on every allowed cross-origin response (echo the specific
		// origin, not "*", so credentials are permitted).
		c.Header("Access-Control-Allow-Origin", origin)
		if cfg.AllowCredentials {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if exposeStr != "" {
			c.Header("Access-Control-Expose-Headers", exposeStr)
		}

		// Preflight (OPTIONS): answer with the allowed methods/headers and stop.
		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Allow-Methods", methodStr)
			c.Header("Access-Control-Allow-Headers", headerStr)
			if cfg.MaxAge > 0 {
				c.Header("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
