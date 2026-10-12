package managementhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIsSPAClientRoute(t *testing.T) {
	for path, want := range map[string]bool{
		"/models": true, "/credentials": true, "/usage/requests": true,
		"/": false, "/v0/management/x": false, "/v1/models": false, "/v1beta/models": false,
		"/assets/a.js": false, "/favicon.ico": false, "/api/x": false, "/oauth/callback": false,
	} {
		if got := isSPAClientRoute(http.MethodGet, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if isSPAClientRoute(http.MethodPost, "/models") {
		t.Error("POST must not fall back")
	}
}

func TestSPAFallbackNoRoute(t *testing.T) {
	engine := gin.New()
	engine.NoRoute(spaFallbackNoRoute(func(c *gin.Context) { c.String(http.StatusOK, "index") }, func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
	}))
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/models", 200, "index"},
		{"/v0/management/nope", 404, `{"error":"not found"}`},
		{"/missing.js", 404, `{"error":"not found"}`},
	} {
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.code || rr.Body.String() != tc.body {
			t.Errorf("%s: %d %q", tc.path, rr.Code, rr.Body.String())
		}
	}
}
