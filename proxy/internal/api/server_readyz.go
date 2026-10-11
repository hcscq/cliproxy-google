package api

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

// readinessError reports why the node should not receive traffic yet.
// In Home mode the node is ready only while a Home client exists and its
// heartbeat is healthy; credentials and scheduling come from Home.
func (s *Server) readinessError() string {
	if s == nil {
		return "server unavailable"
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil {
		return "config not loaded"
	}
	if cfg.Home.Enabled {
		if !homeHealthy() && !homeWithinReadyGrace() {
			if home.Current() == nil {
				return "home client not connected"
			}
			return "home heartbeat not healthy"
		}
	}
	return ""
}

func (s *Server) readyzHandler(c *gin.Context) {
	if reason := s.readinessError(); reason != "" {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": reason, "usage_queue": redisqueue.UsageStats(), "held_requests": homeHoldWaiting.Load()})
		return
	}
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready", "usage_queue": redisqueue.UsageStats(), "held_requests": homeHoldWaiting.Load()})
}
