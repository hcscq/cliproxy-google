package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GetAntigravityCatalog returns per-credential upstream catalog status.
func (h *Handler) GetAntigravityCatalog(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime unavailable"})
		return
	}
	interval := h.runtime.AntigravityCatalogInterval()
	c.JSON(http.StatusOK, gin.H{
		"interval_seconds": int64(interval / time.Second),
		"leader":           h.runtime.CatalogLeader(),
		"credentials":      h.runtime.AntigravityCatalogStatuses(),
	})
}

// RefreshAntigravityCatalog fetches catalogs now (all credentials, or one via auth_id).
func (h *Handler) RefreshAntigravityCatalog(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime unavailable"})
		return
	}
	var body struct {
		AuthID string `json:"auth_id"`
	}
	_ = c.ShouldBindJSON(&body)
	statuses := h.runtime.RefreshAntigravityCatalogs(c.Request.Context(), strings.TrimSpace(body.AuthID))
	c.JSON(http.StatusOK, gin.H{
		"interval_seconds": int64(h.runtime.AntigravityCatalogInterval() / time.Second),
		"leader":           h.runtime.CatalogLeader(),
		"credentials":      statuses,
	})
}
