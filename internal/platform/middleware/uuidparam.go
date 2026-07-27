package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// ValidateUUIDParam rejects path parameters that are not well-formed UUIDs.
//
// WHY THIS EXISTS: every repository interpolates ids as `$1::uuid`. Postgres
// rejects a malformed value with SQLSTATE 22P02 (invalid_text_representation),
// which is NOT pgx.ErrNoRows — so it escaped the not-found branch in every
// handler and surfaced as a 500. A client typo should never be reported as an
// internal server error: 5xx means "we broke", and this is squarely "you asked
// for something that cannot exist".
//
// Fixing it here rather than in ~20 handlers means the guarantee holds for routes
// added later too, and no repository needs to know about SQLSTATE codes.
//
// It returns 404, not 400, for the same reason the object-level checks do: a
// malformed id cannot identify a real record, and 404 reveals nothing about what
// does or does not exist.
func ValidateUUIDParam(names ...string) gin.HandlerFunc {
	if len(names) == 0 {
		names = []string{"id"}
	}
	return func(c *gin.Context) {
		for _, n := range names {
			v := c.Param(n)
			// Only validate params that are actually present on this route.
			if v == "" {
				continue
			}
			if _, err := uuid.Parse(v); err != nil {
				common.Error(c, http.StatusNotFound, "resource not found", "NOT_FOUND")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}
