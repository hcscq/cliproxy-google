package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyzLocalModeReady(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestReadyzHomeModeWithoutClientNotReady(t *testing.T) {
	server := newTestServer(t)
	server.cfgMu.Lock()
	server.cfg.Home.Enabled = true
	server.cfgMu.Unlock()
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
}
