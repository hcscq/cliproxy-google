package middleware

import (
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
)

// Large request bodies (multi-MB prompts / inline images) are copied several times while
// being translated and logged. Many of them at once OOM-killed proxies with a 1Gi limit.
// This bounds the sum of in-flight request bodies; requests that would exceed the budget get
// a fast 429 so the caller (e.g. new-api) retries elsewhere instead of the pod dying.
const (
	defaultBodyBudgetBytes  = 384 << 20
	largeBodyThresholdBytes = 1 << 20
)

type bodyBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

func (b *bodyBudget) acquire(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A single body larger than the whole budget is still allowed when nothing else is in flight.
	if b.used > 0 && b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

func (b *bodyBudget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

// RequestBodyBudgetMiddleware limits the total size of large in-flight request bodies.
// Budget comes from PROXY_BODY_BUDGET_BYTES (default 384MiB); 0 or negative disables it.
func RequestBodyBudgetMiddleware() gin.HandlerFunc {
	limit := int64(defaultBodyBudgetBytes)
	if raw := os.Getenv("PROXY_BODY_BUDGET_BYTES"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			limit = v
		}
	}
	return newBodyBudgetMiddleware(limit)
}

func newBodyBudgetMiddleware(limit int64) gin.HandlerFunc {
	if limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	budget := &bodyBudget{limit: limit}
	return func(c *gin.Context) {
		n := c.Request.ContentLength
		if n < largeBodyThresholdBytes {
			c.Next()
			return
		}
		if !budget.acquire(n) {
			c.Header("Retry-After", "2")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
				"code": http.StatusTooManyRequests, "status": "RESOURCE_EXHAUSTED",
				"message": "proxy is busy with other large requests; retry shortly",
			}})
			return
		}
		defer budget.release(n)
		c.Next()
	}
}
