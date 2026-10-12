package home

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestCoolingQuotaPairs(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{ID: "a", Provider: "antigravity", ModelStates: map[string]*coreauth.ModelState{
		"m429": {Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &coreauth.Error{HTTPStatus: 429}},
		"m503": {Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &coreauth.Error{HTTPStatus: 503}},
		"done": {Unavailable: true, NextRetryAfter: now.Add(-time.Second), LastError: &coreauth.Error{HTTPStatus: 429}},
	}}
	other := &coreauth.Auth{ID: "b", Provider: "codex", ModelStates: a.ModelStates}
	got := coolingQuotaPairs([]*coreauth.Auth{a, other}, now)
	if len(got) != 1 || got[0].model != "m429" {
		t.Fatalf("got %+v", got)
	}
}

func TestQuotaProbeHintAndSend(t *testing.T) {
	d, at := quotaProbeHint(nil, []byte(`{"error":{"details":[{"retryDelay":"12s"}]}}`), time.Now())
	if d == nil || *d != 12*time.Second || at != nil {
		t.Fatalf("retryDelay not parsed")
	}
	if d, at = quotaProbeHint(nil, []byte(`{"error":{"code":429}}`), time.Now()); d != nil || at != nil {
		t.Fatalf("expected no hint")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != quotaProbeGeneratePath || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(429)
	}))
	defer srv.Close()
	old := antigravityModelsURL
	antigravityModelsURL = srv.URL + antigravityModelsEndpointPath
	defer func() { antigravityModelsURL = old }()
	auth := &coreauth.Auth{ID: "a", Metadata: map[string]any{"access_token": "tok", "project_id": "p"}}
	code, _, _, err := sendQuotaProbe(context.Background(), auth, "m")
	if err != nil || code != 429 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
