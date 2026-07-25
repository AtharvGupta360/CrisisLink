package middleware

import "github.com/gin-gonic/gin"

// ServedByHeader carries which api replica actually handled a request. It exists
// purely to make load-balancing OBSERVABLE: with the gateway round-robining across
// replicas, this header is how you (and an interviewer) can watch consecutive
// requests land on different processes. In production you would not expose your
// topology like this — it is a teaching/demo affordance.
const ServedByHeader = "X-Served-By"

// ServedBy stamps every response with this replica's id. The id is whatever the
// process was started with (its port, a pod name under k8s, etc.); an empty id
// disables the header so a single-process run stays clean.
func ServedBy(id string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if id != "" {
			c.Header(ServedByHeader, id)
		}
		c.Next()
	}
}
