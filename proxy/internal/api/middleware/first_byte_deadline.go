package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// DefaultFirstByteDeadline bounds how long a model request may run before the
// client receives its first byte (dispatch + Home/Redis calls + every
// credential attempt). Past it the request is cancelled and a retryable 503 is
// returned so the gateway in front (new-api) fails over instead of hanging.
const DefaultFirstByteDeadline = 120 * time.Second

// DefaultFirstByteDeadlineNonStream is the same bound for non-streaming
// requests, whose first byte only arrives after the whole generation; it sits
// above the default non-stream attempt timeout (180s).
const DefaultFirstByteDeadlineNonStream = 240 * time.Second

var (
	firstByteDeadlineNanos          atomic.Int64
	firstByteDeadlineNonStreamNanos atomic.Int64
)

func init() {
	firstByteDeadlineNanos.Store(int64(DefaultFirstByteDeadline))
	firstByteDeadlineNonStreamNanos.Store(int64(DefaultFirstByteDeadlineNonStream))
}

// SetFirstByteDeadlineNonStreamSeconds applies
// routing.retry.first-byte-deadline-nonstream: 0 = default (240s), <0 = off.
func SetFirstByteDeadlineNonStreamSeconds(sec int) {
	switch {
	case sec == 0:
		firstByteDeadlineNonStreamNanos.Store(int64(DefaultFirstByteDeadlineNonStream))
	case sec < 0:
		firstByteDeadlineNonStreamNanos.Store(0)
	default:
		firstByteDeadlineNonStreamNanos.Store(int64(time.Duration(sec) * time.Second))
	}
}

// FirstByteDeadlineNonStream returns the active non-stream deadline (0 = off).
func FirstByteDeadlineNonStream() time.Duration {
	return time.Duration(firstByteDeadlineNonStreamNanos.Load())
}

// requestIsStreaming reports whether the client asked for a streamed answer
// (Gemini streamGenerateContent / alt=sse, or "stream": true in the body).
// The body is read and restored.
func requestIsStreaming(r *http.Request) bool {
	if strings.Contains(r.URL.Path, "streamGenerateContent") || r.URL.Query().Get("alt") == "sse" {
		return true
	}
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	compact := bytes.ReplaceAll(body, []byte(" "), nil)
	return bytes.Contains(compact, []byte(`"stream":true`))
}

// SetFirstByteDeadlineSeconds applies routing.retry.first-byte-deadline:
// 0 = default (120s), negative = disabled.
func SetFirstByteDeadlineSeconds(sec int) {
	switch {
	case sec == 0:
		firstByteDeadlineNanos.Store(int64(DefaultFirstByteDeadline))
	case sec < 0:
		firstByteDeadlineNanos.Store(0)
	default:
		firstByteDeadlineNanos.Store(int64(time.Duration(sec) * time.Second))
	}
}

// FirstByteDeadline returns the active deadline (0 = disabled).
func FirstByteDeadline() time.Duration { return time.Duration(firstByteDeadlineNanos.Load()) }

func firstByteDeadlineApplies(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	p := r.URL.Path
	return strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/v1beta/") || strings.HasPrefix(p, "/api/provider/")
}

type firstByteWriter struct {
	gin.ResponseWriter
	committed atomic.Bool // first byte / header reached the client
	expired   atomic.Bool // deadline fired before commit: swallow handler output
}

func (w *firstByteWriter) WriteHeader(code int) {
	if w.expired.Load() {
		return
	}
	w.committed.Store(true)
	w.ResponseWriter.WriteHeader(code)
}

func (w *firstByteWriter) WriteHeaderNow() {
	if w.expired.Load() {
		return
	}
	w.committed.Store(true)
	w.ResponseWriter.WriteHeaderNow()
}

func (w *firstByteWriter) Write(b []byte) (int, error) {
	if w.expired.Load() {
		return len(b), nil
	}
	w.committed.Store(true)
	return w.ResponseWriter.Write(b)
}

func (w *firstByteWriter) WriteString(s string) (int, error) {
	if w.expired.Load() {
		return len(s), nil
	}
	w.committed.Store(true)
	return w.ResponseWriter.WriteString(s)
}

func (w *firstByteWriter) Flush() {
	if w.expired.Load() {
		return
	}
	w.ResponseWriter.Flush()
}

// FirstByteDeadlineMiddleware cancels a model request that has not produced a
// first byte within FirstByteDeadline and answers 503 + Retry-After so the
// caller can fail over. Streams that already started are never cut.
func FirstByteDeadlineMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !firstByteDeadlineApplies(c.Request) {
			c.Next()
			return
		}
		limit := FirstByteDeadline()
		if !requestIsStreaming(c.Request) {
			limit = FirstByteDeadlineNonStream()
		}
		if limit <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithCancel(c.Request.Context())
		defer cancel()
		w := &firstByteWriter{ResponseWriter: c.Writer}
		c.Writer = w
		c.Request = c.Request.WithContext(ctx)
		timer := time.AfterFunc(limit, func() {
			if w.committed.Load() {
				return
			}
			w.expired.Store(true)
			if w.committed.Load() { // handler committed concurrently: let it finish
				w.expired.Store(false)
				return
			}
			cancel()
		})
		c.Next()
		timer.Stop()
		if w.expired.Load() && !w.committed.Load() {
			log.WithContext(ctx).Warnf("request exceeded first-byte deadline %s without output; returning 503 for failover (%s %s)", limit, c.Request.Method, c.Request.URL.Path)
			h := w.ResponseWriter.Header()
			for k := range h {
				delete(h, k)
			}
			h.Set("Content-Type", "application/json")
			h.Set("Retry-After", "1")
			w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.ResponseWriter.Write([]byte(`{"error":{"code":"request_deadline_exceeded","message":"no upstream output within the request deadline; retry on another channel","type":"server_error"}}`))
		}
	}
}
