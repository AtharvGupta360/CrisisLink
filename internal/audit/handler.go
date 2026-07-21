package audit

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// List handles GET /admin/audit?aggregateType=&aggregateId=&limit= — the trail.
func (h *Handler) List(c *gin.Context) {
	entries, err := h.repo.List(c.Request.Context(),
		c.Query("aggregateType"), c.Query("aggregateId"),
		common.ClampInt(c.Query("limit"), 50, 1, 200))
	if err != nil {
		common.Error(c, http.StatusInternalServerError, "could not read audit log", "INTERNAL_ERROR")
		return
	}
	common.Success(c, http.StatusOK, "audit trail", entries)
}
