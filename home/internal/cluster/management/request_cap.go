package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

// SetRequestCap: POST /credentials/request-cap {ids, mode: inherit|custom|unlimited, hourly, daily}
func (h *Handler) SetRequestCap(c *gin.Context) {
	var req struct {
		IDs    []string `json:"ids"`
		Mode   string   `json:"mode"`
		Hourly int64    `json:"hourly"`
		Daily  int64    `json:"daily"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "ids is required", err)
		return
	}
	if len(req.IDs) > 10000 {
		respondError(c, http.StatusBadRequest, "too many ids (max 10000)", nil)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "inherit", "", "custom", "unlimited":
	default:
		respondError(c, http.StatusBadRequest, "mode must be inherit|custom|unlimited", nil)
		return
	}
	if req.Hourly < 0 || req.Daily < 0 {
		respondError(c, http.StatusBadRequest, "caps must be >= 0", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	updated, unchanged := 0, 0
	var failed []string
	for _, id := range req.IDs {
		auth, err := h.findOAuthAuth(ctx, authIdentifier{ID: id, Name: id})
		if err != nil || auth == nil {
			failed = append(failed, id)
			continue
		}
		if auth.Metadata == nil {
			auth.Metadata = map[string]any{}
		}
		before := [3]any{auth.Metadata[home.RequestCapModeKey], auth.Metadata[home.RequestCapHourlyKey], auth.Metadata[home.RequestCapDailyKey]}
		delete(auth.Metadata, home.RequestCapModeKey)
		delete(auth.Metadata, home.RequestCapHourlyKey)
		delete(auth.Metadata, home.RequestCapDailyKey)
		switch mode {
		case "custom":
			auth.Metadata[home.RequestCapModeKey] = "custom"
			auth.Metadata[home.RequestCapHourlyKey] = req.Hourly
			auth.Metadata[home.RequestCapDailyKey] = req.Daily
		case "unlimited":
			auth.Metadata[home.RequestCapModeKey] = "unlimited"
		}
		after := [3]any{auth.Metadata[home.RequestCapModeKey], auth.Metadata[home.RequestCapHourlyKey], auth.Metadata[home.RequestCapDailyKey]}
		if fmtAny(before) == fmtAny(after) {
			unchanged++
			continue
		}
		auth.UpdatedAt = time.Now().UTC()
		if _, err := h.repo.UpsertAuth(ctx, auth, "update"); err != nil {
			failed = append(failed, id)
			continue
		}
		updated++
	}
	if updated > 0 {
		_ = h.refreshAuths(ctx)
		if h.runtime != nil {
			h.runtime.ResyncRequestCaps(ctx)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "updated": updated, "unchanged": unchanged, "failed": nonNilStrings(failed)})
}

func fmtAny(v [3]any) string {
	out := ""
	for _, x := range v {
		switch t := x.(type) {
		case nil:
			out += "|"
		case string:
			out += t + "|"
		default:
			out += strings.TrimSpace(strings.Trim(strings.ReplaceAll(toJSONString(t), "\"", ""), " ")) + "|"
		}
	}
	return out
}

func toJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
