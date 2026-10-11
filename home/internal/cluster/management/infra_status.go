package management

import (
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

// GetInfraStatus reports Redis, the usage stream and the PostgreSQL pool /
// pgbouncer as seen by the Home serving this request.
func (h *Handler) GetInfraStatus(c *gin.Context) {
	ctx := c.Request.Context()
	out := gin.H{
		"home":     net.JoinHostPort(h.nodeIP, strconv.Itoa(h.nodePort)),
		"redis":    redisx.Default().Status(ctx),
		"postgres": h.repo.PoolStatus(ctx),
	}
	if h.runtime != nil {
		out["usage_stream"] = h.runtime.UsageStreamStatus(ctx)
	}
	c.JSON(http.StatusOK, out)
}
