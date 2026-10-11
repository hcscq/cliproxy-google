package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/logretention"
)

// UsageRetention reports request-log storage: partitions, sizes and retention.
func (h *Handler) UsageRetention(c *gin.Context) {
	if h == nil || h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repository unavailable"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	configured := logretention.Configured()
	status, err := h.repo.UsageRetentionStatus(ctx, cluster.RequestLogRetentionDays(configured))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": configured, "status": status})
}
