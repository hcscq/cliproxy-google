package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestConfigV8SharedUpstreamDatabaseRoundTrip(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	request := func(method, path, body string, want int) string {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/v8/management/config/"+path, strings.NewReader(body)))
		if response.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.Code, want, response.Body.String())
		}
		return response.Body.String()
	}
	request(http.MethodPut, "upstream/codex", `{"stream-bootstrap-buffering":true,"response-steering":true}`, http.StatusOK)
	request(http.MethodPut, "oauth/auth-auto-refresh-workers", "3", http.StatusOK)
	request(http.MethodPut, "oauth/providers/codex/header-defaults", `{"user-agent":"oauth-agent"}`, http.StatusOK)
	if got := request(http.MethodGet, "upstream/codex/stream-bootstrap-buffering", "", http.StatusOK); got != "true" {
		t.Fatalf("shared provider value was not persisted: %s", got)
	}
	cfg, _, errLoad := repo.LoadConfigAsRuntimeConfig(context.Background())
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !cfg.Codex.StreamBootstrapBuffering || !cfg.Codex.ResponseSteering || cfg.AuthAutoRefreshWorkers != 3 || cfg.OAuthOnlyFields["codex.response-steering"] || !cfg.OAuthOnlyFields["codex-header-defaults.user-agent"] {
		t.Fatal("database projection changed shared values or OAuth-only header scope")
	}
	request(http.MethodPut, "upstream/codex/response-steering", "false", http.StatusOK)
	request(http.MethodPut, "oauth/providers/codex/response-steering", "true", http.StatusOK)
	if got := request(http.MethodGet, "upstream/codex/response-steering", "", http.StatusOK); got != "true" {
		t.Fatal("historical path did not update the shared field")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"oauth":{"providers":{"codex":{"response-steering":false}}}}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("historical request body failed: %s", response.Body.String())
	}
	if got := request(http.MethodGet, "upstream/codex/response-steering", "", http.StatusOK); got != "false" {
		t.Fatal("historical request body did not replace the canonical value")
	}
	request(http.MethodDelete, "upstream/codex", "", http.StatusOK)
	cfg, _, errLoad = repo.LoadConfigAsRuntimeConfig(context.Background())
	if errLoad != nil || cfg.Codex.StreamBootstrapBuffering || cfg.Codex.ResponseSteering || cfg.CodexHeaderDefaults.UserAgent != "oauth-agent" {
		t.Fatalf("deleting shared settings changed unrelated OAuth headers: %v", errLoad)
	}
}
