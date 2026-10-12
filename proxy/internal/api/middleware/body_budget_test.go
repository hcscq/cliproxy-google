package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBodyBudgetRejectsWhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	entered := make(chan struct{})
	r := gin.New()
	r.Use(newBodyBudgetMiddleware(3 << 20))
	r.POST("/", func(c *gin.Context) {
		if c.Query("hold") == "1" {
			close(entered)
			<-release
		}
		c.Status(200)
	})
	big := strings.Repeat("x", 2<<20)
	done := make(chan int)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/?hold=1", strings.NewReader(big)))
		done <- w.Code
	}()
	<-entered
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big)))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second large body = %d, want 429", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small")))
	if w.Code != 200 {
		t.Fatalf("small body = %d", w.Code)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("held = %d", code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big)))
	if w.Code != 200 {
		t.Fatalf("after release = %d", w.Code)
	}
}
