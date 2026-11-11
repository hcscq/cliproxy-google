package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// GetSysPromptSample returns the current in-memory systemInstruction sample
// aggregate (bounded, opt-in, auto-expiring). Read-only; each proxy pod keeps
// its own counts, so callers must query and merge every pod.
func (h *Handler) GetSysPromptSample(c *gin.Context) {
	c.JSON(http.StatusOK, helps.SysPromptSampleSnapshot())
}
