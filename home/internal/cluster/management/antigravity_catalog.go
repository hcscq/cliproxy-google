package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
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

// RefreshSubscriptionTiers re-checks the subscription tier of the given
// credentials now (manual / bulk action; at most 50 per call).
func (h *Handler) RefreshSubscriptionTiers(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime unavailable"})
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids is required"})
		return
	}
	if len(body.IDs) > 50 {
		body.IDs = body.IDs[:50]
	}
	type result struct {
		ID       string `json:"id"`
		OK       bool   `json:"ok"`
		Tier     string `json:"tier,omitempty"`
		TierName string `json:"tier_name,omitempty"`
	}
	out := make([]result, 0, len(body.IDs))
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		tier, name, ok := h.runtime.RefreshSubscriptionTierNow(c.Request.Context(), id)
		out = append(out, result{ID: id, OK: ok, Tier: tier, TierName: name})
	}
	_ = h.refreshAuths(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"results": out})
}

// GetCredentialQuotaGroups returns the resolved quota groups of one credential.
func (h *Handler) GetCredentialQuotaGroups(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime unavailable"})
		return
	}
	id := strings.TrimSpace(c.Param("credential_id"))
	c.JSON(http.StatusOK, gin.H{"groups": h.runtime.CredentialQuotaGroups(id), "stats": home.QuotaGroupStats()})
}
