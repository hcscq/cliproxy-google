package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const tosBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://example.invalid/appeal?x=1","appeal_url_link_text":"Submit Appeal"}}]}}`

func resetTOSPause(t *testing.T) {
	t.Helper()
	antigravityTOSPauseUntil.Clear()
	t.Cleanup(func() { antigravityTOSPauseUntil.Clear() })
}

func TestParseAntigravityTOSViolation(t *testing.T) {
	v := parseAntigravityTOSViolation([]byte(tosBody))
	if v == nil || v.AppealURLText != "Submit Appeal" {
		t.Fatalf("v=%#v", v)
	}
	for _, body := range []string{
		`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`,
		`{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`,
		`{"error":{"code":403,"status":"PERMISSION_DENIED"}}`,
		`not json`,
	} {
		if parseAntigravityTOSViolation([]byte(body)) != nil {
			t.Fatalf("false positive for %s", body)
		}
	}
}

func TestAntigravityTOSPauseWindowDefaultsOff(t *testing.T) {
	for _, raw := range []string{"", "0", "off", "OFF", "false", "none", "disabled", "garbage", "-5m"} {
		cfg := &config.Config{}
		cfg.Antigravity.TOSPauseWindow = raw
		if _, enabled := antigravityTOSPauseWindow(cfg); enabled {
			t.Fatalf("%q must not enable the pause", raw)
		}
	}
	if _, enabled := antigravityTOSPauseWindow(nil); enabled {
		t.Fatal("nil cfg must not enable the pause")
	}
	cfg := &config.Config{}
	cfg.Antigravity.TOSPauseWindow = "30m"
	if w, enabled := antigravityTOSPauseWindow(cfg); !enabled || w != 30*time.Minute {
		t.Fatalf("w=%v enabled=%v", w, enabled)
	}
}

func TestObserveTOSDisabledNeverPauses(t *testing.T) {
	resetTOSPause(t)
	kv := &fakeAntigravityKVClient{values: map[string][]byte{}}
	useFakeAntigravityKVClient(t, kv, true, nil)
	e := NewAntigravityExecutor(&config.Config{})
	e.observeAntigravityTOS(&cliproxyauth.Auth{ID: "a1"}, http.StatusForbidden, []byte(tosBody))
	if antigravityInTOSPause(e.cfg) {
		t.Fatal("pause must stay off by default")
	}
	if _, ok := kv.values[antigravityTOSPauseKVKey]; ok {
		t.Fatal("disabled pause must not write KV")
	}
}

func TestObserveTOSEnabledArmsSharedPauseOnlyForTOS(t *testing.T) {
	resetTOSPause(t)
	kv := &fakeAntigravityKVClient{values: map[string][]byte{}}
	useFakeAntigravityKVClient(t, kv, true, nil)
	cfg := &config.Config{}
	cfg.Antigravity.TOSPauseWindow = "10m"
	e := NewAntigravityExecutor(cfg)
	// 429 and non-TOS 403 never arm.
	e.observeAntigravityTOS(nil, http.StatusTooManyRequests, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`))
	e.observeAntigravityTOS(nil, http.StatusForbidden, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"nope"}}`))
	if antigravityInTOSPause(cfg) {
		t.Fatal("non-TOS errors must not arm the pause")
	}
	e.observeAntigravityTOS(&cliproxyauth.Auth{ID: "a1"}, http.StatusForbidden, []byte(tosBody))
	if !antigravityInTOSPause(cfg) {
		t.Fatal("TOS must arm the pause when enabled")
	}
	if _, ok := kv.values[antigravityTOSPauseKVKey]; !ok {
		t.Fatal("pause must be shared through home KV")
	}
	// Another pod: no local state, reads KV.
	antigravityTOSPauseUntil.Clear()
	if !antigravityInTOSPause(cfg) {
		t.Fatal("pause must be visible from KV on another pod")
	}
	// Turning the config off lifts the pause immediately.
	cfg.Antigravity.TOSPauseWindow = "off"
	if antigravityInTOSPause(cfg) {
		t.Fatal("disabling config must lift the pause")
	}
}

func TestExecuteShortCircuitsDuringTOSPause(t *testing.T) {
	resetTOSPause(t)
	useFakeAntigravityKVClient(t, &fakeAntigravityKVClient{values: map[string][]byte{}}, false, nil)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(tosBody))
	}))
	defer srv.Close()
	cfg := &config.Config{}
	cfg.Antigravity.TOSPauseWindow = "5m"
	cfg.RequestRetry = 1
	e := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "tos-auth", Attributes: map[string]string{"base_url": srv.URL}, Metadata: map[string]any{"access_token": "tok", "project_id": "project-1", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}}
	req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}
	_, err := e.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected upstream 403")
	}
	first := atomic.LoadInt32(&hits)
	if first == 0 {
		t.Fatal("executor did not reach the fake upstream")
	}
	_, err = e.Execute(context.Background(), auth, req, opts)
	if err == nil || !strings.Contains(err.Error(), antigravityTOSPausedMsg) {
		t.Fatalf("second call err=%v, want pause error", err)
	}
	if atomic.LoadInt32(&hits) != first {
		t.Fatal("paused pool must not call upstream")
	}
}

func TestExecuteDefaultConfigKeepsCallingUpstreamAfterTOS(t *testing.T) {
	resetTOSPause(t)
	useFakeAntigravityKVClient(t, &fakeAntigravityKVClient{values: map[string][]byte{}}, false, nil)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(tosBody))
	}))
	defer srv.Close()
	e := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{ID: "tos-auth-2", Attributes: map[string]string{"base_url": srv.URL}, Metadata: map[string]any{"access_token": "tok", "project_id": "project-1", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}}
	req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}
	for i := 0; i < 2; i++ {
		_, err := e.Execute(context.Background(), auth, req, opts)
		if err == nil || strings.Contains(err.Error(), antigravityTOSPausedMsg) {
			t.Fatalf("call %d err=%v, want upstream 403 (pause is off by default)", i, err)
		}
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("hits=%d, default config must not pause", hits)
	}
}
