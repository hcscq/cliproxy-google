package management

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"gorm.io/gorm"
)

// RefreshAuthFiles uses the Home refresh coordinator so refreshed credentials
// follow the database ownership and cross-node fencing rules.
func (h *Handler) RefreshAuthFiles(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		All       bool   `json:"all"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if errBind := c.ShouldBindJSON(&req); errBind != nil && !errors.Is(errBind, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	}
	req.All = req.All || c.Query("all") == "true"
	if req.Name == "" {
		req.Name = c.Query("name")
	}
	if req.AuthIndex == "" {
		req.AuthIndex = c.Query("auth_index")
	}
	if !req.All && strings.TrimSpace(req.Name) == "" && strings.TrimSpace(req.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name or all=true is required"})
		return
	}
	ctx := c.Request.Context()
	auths, errAuths := h.repo.ListAuths(ctx)
	if errAuths != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuths)
		return
	}
	if !req.All {
		auth := findOAuthAuthInList(auths, authIdentifier{ID: strings.TrimSpace(req.AuthIndex), Name: strings.TrimSpace(req.Name)})
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
			return
		}
		auths = []*coreauth.Auth{auth}
	}
	results := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if !isOAuthAuth(auth) || (req.All && auth.Disabled) {
			continue
		}
		_, errRefresh := h.runtime.RefreshNow(ctx, auth.ID)
		if !req.All {
			if errRefresh != nil {
				respondError(c, http.StatusInternalServerError, "refresh_failed", errRefresh)
				return
			}
			updated, errFind := h.findOAuthAuth(ctx, authIdentifier{ID: auth.ID})
			if errFind != nil {
				respondError(c, http.StatusInternalServerError, "auth_load_failed", errFind)
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "auth": authFileEntry(updated)})
			return
		}
		result := gin.H{"id": auth.ID, "success": errRefresh == nil}
		if errRefresh != nil {
			result["error"] = "refresh failed"
		}
		results = append(results, result)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "results": results})
}

// ResetQuota accepts the CPA auth_index contract and clears authoritative Home
// cooldowns, rather than mutating a separate SDK auth manager.
func (h *Handler) ResetQuota(c *gin.Context) {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(body.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	if h.runtime == nil || h.runtime.CoreManager() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	result, errClear := h.runtime.CoreManager().ClearQuotaCooldown(ctx, strings.TrimSpace(body.AuthIndex), "")
	if errClear != nil {
		status := quotaReadErrorStatus(errClear)
		if errors.Is(errClear, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		respondError(c, status, "cooldown_reset_failed", errClear)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_index": result.CredentialID, "models": append([]string{}, result.ClearedModels...)})
}
