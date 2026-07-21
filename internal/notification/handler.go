package notification

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

type NotificationHandler struct {
	svc *NotificationService
}

func NewNotificationHandler(svc *NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List handles GET /admin/notifications?limit= — what the event consumer produced.
// Exactly one notification per event, even if Kafka delivered it twice.
func (h *NotificationHandler) List(c *gin.Context) {
	items, err := h.svc.ListRecent(c.Request.Context(), common.ClampInt(c.Query("limit"), 20, 1, 200))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not list notifications", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "notifications", items)
}
