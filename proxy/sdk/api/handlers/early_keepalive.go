package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

const (
	defaultEarlyKeepAlive         = 30 * time.Second
	defaultEarlyKeepAliveInterval = 15 * time.Second
)

// EarlyKeepAliveSettings returns the delay before committing SSE headers for a
// stream that has not started yet (0 = disabled) and the repeat interval.
func (h *BaseAPIHandler) EarlyKeepAliveSettings() (time.Duration, time.Duration) {
	delay, interval := defaultEarlyKeepAlive, defaultEarlyKeepAliveInterval
	if h == nil || h.Cfg == nil || !h.Cfg.Streaming.EarlyKeepAlive {
		return 0, 0 // off by default
	}
	{
		switch s := h.Cfg.Streaming.EarlyKeepAliveSeconds; {
		case s < 0:
			return 0, 0
		case s > 0:
			delay = time.Duration(s) * time.Second
		}
		if s := h.Cfg.Streaming.EarlyKeepAliveIntervalSeconds; s > 0 {
			interval = time.Duration(s) * time.Second
		}
	}
	return delay, interval
}

// StreamStart is the result of starting a streaming execution.
type StreamStart struct {
	Data    <-chan []byte
	Headers http.Header
	Errs    <-chan *interfaces.ErrorMessage
	// Committed reports that 200 + SSE headers (and keep-alives) were already
	// written; errors must then be delivered in-stream.
	Committed bool
	// Aborted reports that the client went away while waiting.
	Aborted bool
}

// StartStreamWithEarlyKeepAlive runs start (which may spend a long time
// switching credentials before the first upstream byte) in the background.
// If it has not returned after delay, it commits the SSE response via
// commit and writes keepAlive every interval until start returns.
func StartStreamWithEarlyKeepAlive(c *gin.Context, flusher http.Flusher, delay, interval time.Duration, commit func(), keepAlive []byte, start func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage)) StreamStart {
	type result struct {
		data <-chan []byte
		hdr  http.Header
		errs <-chan *interfaces.ErrorMessage
	}
	done := make(chan result, 1)
	go func() {
		d, hd, e := start()
		done <- result{d, hd, e}
	}()
	var out StreamStart
	var timer <-chan time.Time
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		timer = t.C
	}
	var tick <-chan time.Time
	for {
		select {
		case r := <-done:
			out.Data, out.Headers, out.Errs = r.data, r.hdr, r.errs
			return out
		case <-c.Request.Context().Done():
			out.Aborted = true
			// Let start finish in the background; its context is cancelled by the caller.
			return out
		case <-timer:
			commit()
			c.Status(http.StatusOK)
			_, _ = c.Writer.Write(keepAlive)
			flusher.Flush()
			out.Committed = true
			if interval > 0 {
				tk := time.NewTicker(interval)
				defer tk.Stop()
				tick = tk.C
			}
		case <-tick:
			_, _ = c.Writer.Write(keepAlive)
			flusher.Flush()
		}
	}
}
