package outbox

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

type OutboxHandler struct {
	svc *OutboxService
}

func NewOutboxHandler(svc *OutboxService) *OutboxHandler {
	return &OutboxHandler{svc: svc}
}

// List handles GET /admin/outbox?limit= — recent outbox events (published or not).
// An ops window onto the event stream; the relay (P20) will flip published_at.
func (h *OutboxHandler) List(c *gin.Context) {
	events, err := h.svc.ListRecent(c.Request.Context(), common.ClampInt(c.Query("limit"), 20, 1, 200))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list outbox events", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "outbox events", events)
}

// ListDead handles GET /admin/outbox/dead — events that exhausted their retry
// budget. This is the relay's dead-letter queue, and an empty list is what healthy
// looks like. The response also carries the current lag so an operator sees both
// "what is stuck" and "how far behind are we" in one call.
func (h *OutboxHandler) ListDead(c *gin.Context) {
	ctx := c.Request.Context()
	events, err := h.svc.ListDead(ctx, common.ClampInt(c.Query("limit"), 20, 1, 200))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list dead events", "INTERNAL_ERROR")
		return
	}
	lag, err := h.svc.PendingLag(ctx)
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not read outbox lag", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "dead-lettered outbox events", gin.H{
		"deadCount":  len(events),
		"pendingLag": lag,
		"events":     events,
	})
}
