package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func qecTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	return &cliproxyauth.Auth{ID: fmt.Sprintf("qec-%s-%d", t.Name(), time.Now().UnixNano())}
}

func dur(d time.Duration) *time.Duration { return &d }

// Switch OFF: long reset must NOT park the credential (byte-identical to old behavior).
func TestQuotaExhaustedCooldownOffIsNoop(t *testing.T) {
	cfg := &config.Config{} // AntigravityQuotaExhaustedCooldown defaults false
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(65 * time.Hour)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if in, _ := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Minute)); in {
		t.Fatalf("switch off should not park the credential")
	}
}

// Switch ON + long reset: park this credential×model (capped at default 24h);
// a different model on the same credential stays usable (model-scoped).
func TestQuotaExhaustedCooldownOnParksModelScoped(t *testing.T) {
	cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityQuotaExhaustedCooldown: true}}
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(65 * time.Hour)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	in, remaining := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Minute))
	if !in {
		t.Fatalf("credential×argon should be parked")
	}
	if remaining > 24*time.Hour || remaining < 23*time.Hour {
		t.Fatalf("remaining should be capped near 24h, got %v", remaining)
	}
	if in2, _ := antigravityIsInShortCooldown(auth, "gemini-2.5-flash-tiered", now.Add(time.Minute)); in2 {
		t.Fatalf("other model must not be parked (model-scoped)")
	}
}

// Short reset (below min threshold) is left to the existing short-cooldown path.
func TestQuotaExhaustedCooldownBelowThreshold(t *testing.T) {
	cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityQuotaExhaustedCooldown: true}}
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(2 * time.Minute)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if in, _ := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Second)); in {
		t.Fatalf("below-threshold reset should not be parked by this path")
	}
}

// Configurable max cap clamps an over-long reset.
func TestQuotaExhaustedCooldownCappedAtConfiguredMax(t *testing.T) {
	cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{
		AntigravityQuotaExhaustedCooldown:           true,
		AntigravityQuotaExhaustedCooldownMaxSeconds: 3600,
	}}
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(65 * time.Hour)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	_, remaining := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Second))
	if remaining > time.Hour || remaining < 59*time.Minute {
		t.Fatalf("remaining should be capped near 1h, got %v", remaining)
	}
}

// Auto-recovery: once the cooldown window passes, the credential is usable again.
func TestQuotaExhaustedCooldownAutoRecovers(t *testing.T) {
	cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{
		AntigravityQuotaExhaustedCooldown:           true,
		AntigravityQuotaExhaustedCooldownMinSeconds: 1,
		AntigravityQuotaExhaustedCooldownMaxSeconds: 600,
	}}
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(5 * time.Minute)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if in, _ := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Minute)); !in {
		t.Fatalf("should be parked within the window")
	}
	if in, _ := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(6*time.Minute)); in {
		t.Fatalf("should auto-recover after the window")
	}
}

// decideAntigravity429 classifies the three real reset forms as FullQuotaExhausted
// with a parsed retryAfter, so the cooldown path receives a long duration.
func TestQuotaExhaustedDecideParsesResetForms(t *testing.T) {
	cases := map[string]string{
		"retryInfo":   `{"error":{"code":429,"message":"Individual quota reached. Resets in 65h33m10s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-4-argon-high"}},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"235990s"}]}}`,
		"resetStamp":  `{"error":{"code":429,"message":"Individual quota reached.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetTimeStamp":"` + time.Now().Add(60*time.Hour).UTC().Format(time.RFC3339) + `"}}]}}`,
		"messageText": `{"error":{"code":429,"message":"Individual quota reached. Resets in 48h0m0s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
	}
	for name, body := range cases {
		dec := decideAntigravity429([]byte(body))
		if dec.kind != antigravity429DecisionFullQuotaExhausted {
			t.Fatalf("%s: kind = %q, want full_quota_exhausted", name, dec.kind)
		}
		if name != "messageText" && (dec.retryAfter == nil || *dec.retryAfter < time.Hour) {
			t.Fatalf("%s: retryAfter should be a long duration, got %v", name, dec.retryAfter)
		}
	}
}

// Env override enables the feature even with the config switch off (reliable
// gradual rollout path).
func TestQuotaExhaustedCooldownEnabledByEnv(t *testing.T) {
	t.Setenv("CPA_QUOTA_EXHAUSTED_COOLDOWN", "1")
	cfg := &config.Config{} // config switch stays off
	auth := qecTestAuth(t)
	now := time.Now()
	dec := antigravity429Decision{kind: antigravity429DecisionFullQuotaExhausted, retryAfter: dur(65 * time.Hour)}
	if err := recordAntigravityQuotaExhaustedCooldown(context.Background(), cfg, auth, "gemini-4-argon-high", dec, now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if in, _ := antigravityIsInShortCooldown(auth, "gemini-4-argon-high", now.Add(time.Minute)); !in {
		t.Fatalf("env override should enable parking")
	}
}
