package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// AffinityStats returns sticky-session outcome counts for the last N hours.
func (h *Handler) AffinityStats(c *gin.Context) {
	if h == nil || h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repository unavailable"})
		return
	}
	hours, _ := strconv.Atoi(strings.TrimSpace(c.Query("hours")))
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	stats, err := h.repo.AffinityStatsSince(ctx, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"hours": hours, "stats": stats})
}

// RequestAffinity returns the sticky-session outcome of one request
// (looked up by request id and/or trace id).
func (h *Handler) RequestAffinity(c *gin.Context) {
	if h == nil || h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repository unavailable"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	rec, ok := h.repo.RequestAffinity(ctx, c.Query("trace_id"), c.Query("request_id"))
	if !ok {
		c.JSON(http.StatusOK, gin.H{"found": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"found": true, "source": rec.Source, "outcome": rec.Outcome, "auth_id": rec.AuthID, "prev_auth_id": rec.PrevAuth, "created_at": rec.CreatedAt})
}
