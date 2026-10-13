package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestStartStreamWithEarlyKeepAlive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	start := func(wait time.Duration) func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		return func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
			time.Sleep(wait)
			return nil, nil, nil
		}
	}
	run := func(wait, delay time.Duration) (StreamStart, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		commit := func() { c.Header("Content-Type", "text/event-stream") }
		res := StartStreamWithEarlyKeepAlive(c, w, delay, 20*time.Millisecond, commit, []byte(": ka\n\n"), start(wait))
		return res, w
	}
	if res, w := run(5*time.Millisecond, 200*time.Millisecond); res.Committed || w.Body.Len() != 0 {
		t.Fatalf("fast start must not commit: %+v %q", res, w.Body.String())
	}
	res, w := run(120*time.Millisecond, 30*time.Millisecond)
	if !res.Committed || w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("slow start must commit: %+v code=%d", res, w.Code)
	}
	if n := strings.Count(w.Body.String(), ": ka\n\n"); n < 3 {
		t.Fatalf("expected initial + periodic keep-alives, got %d: %q", n, w.Body.String())
	}
	if res, _ := run(50*time.Millisecond, 0); res.Committed {
		t.Fatal("delay 0 disables early keep-alive")
	}
}
