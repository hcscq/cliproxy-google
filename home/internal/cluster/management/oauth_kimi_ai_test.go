package management

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

type kimiLoginTransport func(*http.Request) (*http.Response, error)

func (f kimiLoginTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOAuthV8KimiDomainsReachDeviceAuthorization(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	h := NewHandler(cluster.NewRepository(db), nil, "127.0.0.1", 0)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v0/management/kimi-auth-url", h.RequestKimiToken)
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	for _, tc := range []struct{ path, host string }{
		{"/v8/management/oauth/auth-url?provider=kimi-ai", "auth.kimi.ai"},
		{"/v8/management/oauth/auth-url?provider=kimi", "auth.kimi.com"},
		{"/v0/management/kimi-auth-url", "auth.kimi.com"},
	} {
		called := false
		http.DefaultTransport = kimiLoginTransport(func(req *http.Request) (*http.Response, error) {
			called = true
			if req.URL.Host != tc.host || req.URL.Path != "/api/oauth/device_authorization" {
				t.Errorf("device endpoint=%s", req.URL)
			}
			return nil, errors.New("fixture: stop before device flow worker")
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
		if !called || response.Code != 500 {
			t.Fatalf("route %s did not reach Kimi OAuth: called=%v status=%d", tc.path, called, response.Code)
		}
	}
}
