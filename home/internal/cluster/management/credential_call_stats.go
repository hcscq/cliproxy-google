package management

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

// Call-stats windows (?stats_window=1h|24h|7d, default 24h).
var callStatWindows = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

func callStatWindow(v string) (string, time.Duration) {
	v = strings.ToLower(strings.TrimSpace(v))
	if d, ok := callStatWindows[v]; ok {
		return v, d
	}
	return "24h", 24 * time.Hour
}

var callStatsCache struct {
	sync.Mutex
	at    map[string]time.Time
	stats map[string]map[string]*cluster.CredentialCallStats
}

// credentialCallStats is cached per window for credentialStatsTTL (30s).
func (h *Handler) credentialCallStats(ctx context.Context, window string, d time.Duration) map[string]*cluster.CredentialCallStats {
	callStatsCache.Lock()
	defer callStatsCache.Unlock()
	if callStatsCache.at == nil {
		callStatsCache.at, callStatsCache.stats = map[string]time.Time{}, map[string]map[string]*cluster.CredentialCallStats{}
	}
	if cur, ok := callStatsCache.stats[window]; ok && time.Since(callStatsCache.at[window]) < credentialStatsTTL {
		return cur
	}
	stats, err := h.repo.LoadCredentialCallStats(ctx, time.Now().Add(-d))
	if err != nil {
		if cur, ok := callStatsCache.stats[window]; ok {
			return cur
		}
		return map[string]*cluster.CredentialCallStats{}
	}
	callStatsCache.at[window], callStatsCache.stats[window] = time.Now(), stats
	return stats
}

// successRate is ok/calls in [0,1]; ok=false when there were no calls.
func successRate(s *cluster.CredentialCallStats) (float64, bool) {
	if s == nil || s.Calls == 0 {
		return 0, false
	}
	return float64(s.OK) / float64(s.Calls), true
}

// GetCredentialCallStats: GET /credentials/:credential_id/call-stats?stats_window=
// totals plus a per-model split (hourly granularity).
func (h *Handler) GetCredentialCallStats(c *gin.Context) {
	id := strings.TrimSpace(c.Param("credential_id"))
	window, d := callStatWindow(c.Query("stats_window"))
	ctx, cancel := h.requestContext(c)
	defer cancel()
	total := h.credentialCallStats(ctx, window, d)[id]
	if total == nil {
		total = &cluster.CredentialCallStats{}
	}
	models, err := h.repo.LoadCredentialModelCallStats(ctx, id, time.Now().Add(-d))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "stats_failed", err)
		return
	}
	if models == nil {
		models = []cluster.CredentialModelCallStats{}
	}
	c.JSON(http.StatusOK, gin.H{"window": window, "total": total, "models": models})
}
