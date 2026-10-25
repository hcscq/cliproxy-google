package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/errorsamples"
)

// ListErrorSampleTypes returns stored error types with counts and last seen.
func (h *Handler) ListErrorSampleTypes(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	types, err := h.repo.ListErrorSampleTypes(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "error_samples_failed", err)
		return
	}
	var total int64
	for _, t := range types {
		total += t.Bytes
	}
	s := errorsamples.Get()
	c.JSON(http.StatusOK, gin.H{"types": types, "stored_bytes": total, "settings": gin.H{
		"enabled": s.Enabled, "per_type": s.PerType, "max_bytes": s.MaxBytes, "max_types": s.MaxTypes,
		"exclude": s.Exclude, "overrides": s.Overrides,
	}})
}

// GetErrorSamples returns the samples of one type (?type=).
func (h *Handler) GetErrorSamples(c *gin.Context) {
	typ := strings.TrimSpace(c.Query("type"))
	if typ == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type is required"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	rows, err := h.repo.ListErrorSamples(ctx, typ)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "error_samples_failed", err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		item := gin.H{"id": r.ID, "type": r.Type, "created_at": r.CreatedAt, "status": r.Status, "requested_model": r.RequestedModel,
			"request_id": r.RequestID, "proxy_ip": r.ClientIP, "home_ip": r.HomeIP, "size": r.Size}
		if raw, errDecode := cluster.DecodeErrorSample(r.Data); errDecode == nil {
			item["sample"] = json.RawMessage(raw)
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"type": typ, "samples": out})
}

// DeleteErrorSamples clears one type (?type=) or all samples.
func (h *Handler) DeleteErrorSamples(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	n, err := h.repo.DeleteErrorSamples(ctx, strings.TrimSpace(c.Query("type")))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "error_samples_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}
