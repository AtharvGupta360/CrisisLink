package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

type OutboxHandler struct {
	svc *service.OutboxService
}

func NewOutboxHandler(svc *service.OutboxService) *OutboxHandler {
	return &OutboxHandler{svc: svc}
}

// List handles GET /admin/outbox?limit= — recent outbox events (published or not).
// An ops window onto the event stream; the relay (P20) will flip published_at.
func (h *OutboxHandler) List(c *gin.Context) {
	events, err := h.svc.ListRecent(c.Request.Context(), clampInt(c.Query("limit"), 20, 1, 200))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list outbox events", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "outbox events", events)
}
