package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

// Stall detection for liveness. A background probe periodically acquires the
// same locks the request path needs (config read lock and the Home client
// mutex). /livez only reads the last success timestamp, so it still answers
// when those locks are stuck and kubelet can restart the pod.
//
// The probe budget is far longer than any critical section that does not
// perform network I/O, so a busy-but-healthy process keeps updating lastOK.
var (
	stallProbeInterval = time.Second
	stallProbeBudget   = time.Second
	stallFailAfter     = 8 * time.Second
)

func (s *Server) startStallWatch() {
	if s == nil {
		return
	}
	s.stallOnce.Do(func() {
		s.stallStop = make(chan struct{})
		s.stallLastOK.Store(time.Now().UnixNano())
		s.stallStarted.Store(true)
		go s.stallLoop()
	})
}

func (s *Server) stopStallWatch() {
	if s == nil || s.stallStop == nil {
		return
	}
	s.stallStopOnce.Do(func() { close(s.stallStop) })
}

func (s *Server) stallLoop() {
	ticker := time.NewTicker(stallProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stallStop:
			return
		case <-ticker.C:
			if s.probeStall(stallProbeBudget) {
				s.stallLastOK.Store(time.Now().UnixNano())
			}
		}
	}
}

func (s *Server) probeStall(budget time.Duration) bool {
	if s == nil {
		return true
	}
	if !lockRWBefore(&s.cfgMu, budget) {
		return false
	}
	client := home.Current()
	if client == nil {
		return true
	}
	return client.ProbeCriticalSection(budget)
}

func lockRWBefore(mu interface {
	TryRLock() bool
	RUnlock()
}, budget time.Duration) bool {
	if budget <= 0 {
		budget = time.Second
	}
	deadline := time.Now().Add(budget)
	for {
		if mu.TryRLock() {
			mu.RUnlock()
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Server) livezStalled() bool {
	if s == nil || !s.stallStarted.Load() {
		return false
	}
	last := s.stallLastOK.Load()
	if last <= 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) > stallFailAfter
}

func (s *Server) livezHandler(c *gin.Context) {
	if s.livezStalled() {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "stalled"})
		return
	}
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
