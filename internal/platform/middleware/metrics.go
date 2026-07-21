package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
)

// Metrics records request count and latency for every request.
//
// It is placed EARLY in the chain so its timer spans the work of every middleware
// below it — including the rate limiter and auth. Latency the client experiences
// includes time spent being rejected, and a dashboard that only measures
// successful handler time will look healthy while users are timing out.
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// c.FullPath() is the ROUTE TEMPLATE ("/api/v1/incidents/:id"), not the
		// concrete path. This is the difference between ~40 time series and one per
		// incident id ever created. Unbounded label cardinality is the standard way
		// people take down their own Prometheus.
		route := c.FullPath()
		if route == "" {
			// No matched route (404). Bucket them all together rather than labelling
			// by the raw URL, which an attacker could otherwise use to blow up
			// cardinality just by requesting random paths.
			route = "unmatched"
		}

		status := strconv.Itoa(c.Writer.Status())
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, route, status).Inc()
		metrics.HTTPDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(start).Seconds())
	}
}
