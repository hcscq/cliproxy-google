package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLivezStaysShallowUntilWatchArms(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the stall watch starts (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestLivezFailsWhenConfigLockIsStuck(t *testing.T) {
	prevInterval, prevBudget, prevFail := stallProbeInterval, stallProbeBudget, stallFailAfter
	stallProbeInterval = 15 * time.Millisecond
	stallProbeBudget = 25 * time.Millisecond
	stallFailAfter = 70 * time.Millisecond
	t.Cleanup(func() {
		stallProbeInterval, stallProbeBudget, stallFailAfter = prevInterval, prevBudget, prevFail
	})

	server := newTestServer(t)
	server.startStallWatch()
	t.Cleanup(server.stopStallWatch)

	healthy := httptest.NewRecorder()
	server.engine.ServeHTTP(healthy, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if healthy.Code != http.StatusOK {
		t.Fatalf("livez before stall = %d, want 200", healthy.Code)
	}

	server.cfgMu.Lock()
	defer server.cfgMu.Unlock()
	time.Sleep(180 * time.Millisecond)

	health := httptest.NewRecorder()
	server.engine.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("healthz during stall = %d, want 200", health.Code)
	}
	live := httptest.NewRecorder()
	server.engine.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusServiceUnavailable {
		t.Fatalf("livez during stall = %d, want 503 (body=%s)", live.Code, live.Body.String())
	}
}
