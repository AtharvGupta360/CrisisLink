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
