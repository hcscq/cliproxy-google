package home

import (
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

const qeBody = `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com","metadata":{"model":"gemini-2.5-flash-lite","quotaResetTimeStamp":"2099-01-01T00:00:00Z"}}]}}`

func TestUsageResultMarksHiddenModelQuota(t *testing.T) {
	res := coreauth.NewUsageResult("a", "antigravity", "gemini-2.5-flash-lite", 429, qeBody)
	if res.Error == nil || res.Error.Code != coreauth.ModelQuotaExhaustedCode || res.ResetAt == nil {
		t.Fatalf("QE 429 not marked: %+v", res.Error)
	}
	plain := coreauth.NewUsageResult("a", "antigravity", "m", 429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted"}}`)
	if plain.Error.Code != "" {
		t.Fatalf("generic 429 must not be marked: %q", plain.Error.Code)
	}
}

// Regression: a bucket probe showing remaining quota used to release hidden per-model
// QUOTA_EXHAUSTED windows, sending real traffic into repeated 429s every re-probe.
func TestBucketGuardKeepsHiddenModelQuota(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(3 * time.Hour)
	models := map[string][]string{quotaBucketGemini: {"gemini-2.5-flash-lite"}}
	auth := &coreauth.Auth{ID: "a", ModelStates: map[string]*coreauth.ModelState{
		"gemini-2.5-flash-lite": {Unavailable: true, NextRetryAfter: reset, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: reset},
			LastError: &coreauth.Error{Code: coreauth.ModelQuotaExhaustedCode, HTTPStatus: 429}},
	}}
	recovered := map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, ratio: 0.8}}
	if acts := planQuotaBucketActions(auth, models, recovered, 5*time.Minute, true, now); len(acts) != 0 {
		t.Fatalf("hidden per-model quota released by bucket ratio: %+v", acts)
	}
}

func TestBucketRiseNudgesProbe(t *testing.T) {
	r := &Runtime{}
	r.prober.next = map[string]time.Time{}
	r.prober.level = map[string]int{}
	now := time.Now()
	far := now.Add(time.Hour)
	r.prober.next["b|gemini-3-flash"] = far
	auth := &coreauth.Auth{ID: "b", ModelStates: map[string]*coreauth.ModelState{
		"gemini-3-flash": {Unavailable: true, NextRetryAfter: now.Add(2 * time.Hour), LastError: &coreauth.Error{Code: coreauth.ModelQuotaExhaustedCode, HTTPStatus: 429}},
	}}
	if n := r.nudgeProbesOnBucketRise(auth, map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, ratio: 0.2}}, now); n != 0 {
		t.Fatalf("first observation must only record: %d", n)
	}
	if n := r.nudgeProbesOnBucketRise(auth, map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, ratio: 0.2}}, now); n != 0 {
		t.Fatalf("flat ratio nudged: %d", n)
	}
	if n := r.nudgeProbesOnBucketRise(auth, map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, ratio: 0.4}}, now); n != 1 {
		t.Fatalf("rising ratio not nudged: %d", n)
	}
	if !r.prober.next["b|gemini-3-flash"].Before(far) {
		t.Fatal("probe not made due")
	}
}

func TestQuotaProbeBackoffSchedule(t *testing.T) {
	var c *config.Config
	got := c.QuotaProbeBackoff()
	want := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default %v", got)
		}
	}
	for lvl, w := range map[int]time.Duration{0: time.Minute, 1: 2 * time.Minute, 4: 30 * time.Minute, 9: 30 * time.Minute} {
		if s := quotaProbeBackoffStep(got, lvl); s != w {
			t.Fatalf("level %d = %v want %v", lvl, s, w)
		}
	}
	c = &config.Config{QuotaProbeBackoffSchedule: "30s, 3m"}
	if s := c.QuotaProbeBackoff(); len(s) != 2 || s[1] != 3*time.Minute {
		t.Fatalf("custom %v", s)
	}
	c.QuotaProbeBackoffSchedule = "bogus"
	if s := c.QuotaProbeBackoff(); len(s) != 5 {
		t.Fatalf("invalid must fall back: %v", s)
	}
}

func TestQuotaSyncIgnoresOwnOrigin(t *testing.T) {
	r := &Runtime{}
	raw, _ := json.Marshal(quotaSyncMsg{Origin: quotaSyncOrigin, Kind: "cool", AuthID: "x", Model: "m", UntilMS: time.Now().Add(time.Hour).UnixMilli()})
	r.handleQuotaSync(string(raw)) // nil manager + own origin: must be a no-op, no panic
}
