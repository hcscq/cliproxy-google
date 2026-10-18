package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newDeadlineEngine(h gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(FirstByteDeadlineMiddleware())
	e.POST("/v1/chat/completions", h)
	e.POST("/v0/management/x", h)
	e.POST("/v1beta/models/*action", h)
	return e
}

func withDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	firstByteDeadlineNanos.Store(int64(d))
	firstByteDeadlineNonStreamNanos.Store(int64(d))
	t.Cleanup(func() { SetFirstByteDeadlineSeconds(0); SetFirstByteDeadlineNonStreamSeconds(0) })
}

func TestFirstByteDeadlineReturns503WhenHandlerHangs(t *testing.T) {
	withDeadline(t, 50*time.Millisecond)
	e := newDeadlineEngine(func(c *gin.Context) {
		<-c.Request.Context().Done() // simulates a stalled Home/Redis call honoring ctx
		c.JSON(http.StatusInternalServerError, gin.H{"error": "context canceled"})
	})
	start := time.Now()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request_deadline_exceeded") || strings.Contains(rec.Body.String(), "context canceled") {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("took %s", el)
	}
}

func TestFirstByteDeadlineDoesNotCutStartedStream(t *testing.T) {
	withDeadline(t, 50*time.Millisecond)
	e := newDeadlineEngine(func(c *gin.Context) {
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("data: a\n\n")
		c.Writer.Flush()
		select {
		case <-c.Request.Context().Done():
			t.Error("context cancelled after first byte")
		case <-time.After(150 * time.Millisecond):
		}
		_, _ = c.Writer.WriteString("data: b\n\n")
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	if rec.Code != http.StatusOK || rec.Body.String() != "data: a\n\ndata: b\n\n" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFirstByteDeadlineSkipsManagementAndDisabled(t *testing.T) {
	withDeadline(t, 30*time.Millisecond)
	slow := func(c *gin.Context) {
		select {
		case <-c.Request.Context().Done():
			c.Status(499)
		case <-time.After(100 * time.Millisecond):
			c.Status(http.StatusOK)
		}
	}
	e := newDeadlineEngine(slow)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/management/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("management path affected: %d", rec.Code)
	}
	SetFirstByteDeadlineSeconds(-1)
	SetFirstByteDeadlineNonStreamSeconds(-1)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled deadline still applied: %d", rec.Code)
	}
}

func TestSetFirstByteDeadlineSeconds(t *testing.T) {
	t.Cleanup(func() { SetFirstByteDeadlineSeconds(0) })
	SetFirstByteDeadlineSeconds(0)
	if FirstByteDeadline() != DefaultFirstByteDeadline {
		t.Fatal(FirstByteDeadline())
	}
	SetFirstByteDeadlineSeconds(30)
	if FirstByteDeadline() != 30*time.Second {
		t.Fatal(FirstByteDeadline())
	}
	SetFirstByteDeadlineSeconds(-5)
	if FirstByteDeadline() != 0 {
		t.Fatal(FirstByteDeadline())
	}
}

func TestFirstByteDeadlineStreamVsNonStream(t *testing.T) {
	firstByteDeadlineNanos.Store(int64(30 * time.Millisecond))
	firstByteDeadlineNonStreamNanos.Store(int64(time.Second))
	t.Cleanup(func() { SetFirstByteDeadlineSeconds(0); SetFirstByteDeadlineNonStreamSeconds(0) })
	var gotBody string
	e := newDeadlineEngine(func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		gotBody = string(b)
		select {
		case <-c.Request.Context().Done():
		case <-time.After(150 * time.Millisecond):
			c.String(http.StatusOK, "done")
		}
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream": true}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stream: code=%d", rec.Code)
	}
	if gotBody != `{"stream": true}` {
		t.Fatalf("body not restored: %q", gotBody)
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("non-stream should use the longer deadline: code=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash:streamGenerateContent?alt=sse", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gemini stream path: code=%d", rec.Code)
	}
}
