package home

import (
	"context"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

func ratio(v float64) *float64 { return &v }

func TestAntigravityModelBucket(t *testing.T) {
	cases := map[string]string{
		"gemini-2.5-flash": quotaBucketGemini, "gemini-3.1-pro-low": quotaBucketGemini,
		"claude-sonnet-4-6": quotaBucketThirdParty, "gpt-oss-120b-medium": quotaBucketThirdParty,
		"chat_20706": "", "": "",
	}
	for model, want := range cases {
		if got := antigravityModelBucket(model); got != want {
			t.Errorf("bucket(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestQuotaBucketVerdicts(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(5 * 24 * time.Hour)
	v := quotaBucketVerdicts([]QuotaBucketWindow{
		{ScopeID: "gemini", RemainingRatio: ratio(0.4)},
		{ScopeID: "gemini", RemainingRatio: ratio(0), ResetAt: &reset},
		{ScopeID: "third-party", RemainingRatio: ratio(0.73)},
	}, now)
	if g := v[quotaBucketGemini]; !g.known || !g.exhausted || !g.resetAt.Equal(reset) {
		t.Fatalf("gemini verdict = %+v", g)
	}
	if p := v[quotaBucketThirdParty]; !p.known || p.exhausted {
		t.Fatalf("third-party verdict = %+v", p)
	}
}

func TestApplyQuotaBucketWindowsBlocksAndReleasesPerBucket(t *testing.T) {
	const authID = "bucket-guard-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: authID, Index: authID, Provider: "antigravity", Status: coreauth.StatusActive, Label: "keepim@example"}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-2.5-flash"}, {ID: "gemini-3-flash"}, {ID: "claude-sonnet-4-6"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	rt := &Runtime{coreManager: manager}
	ctx := context.Background()
	reset := time.Now().Add(5 * 24 * time.Hour)

	rt.ApplyQuotaBucketWindows(ctx, authID, []QuotaBucketWindow{
		{ScopeID: "gemini", RemainingRatio: ratio(0), ResetAt: &reset},
		{ScopeID: "third-party", RemainingRatio: ratio(0.73)},
	})
	got, _ := manager.GetByID(authID)
	for _, m := range []string{"gemini-2.5-flash", "gemini-3-flash"} {
		st := got.ModelStates[m]
		if !isQuotaBucketState(st) || !st.Unavailable || !st.Quota.Exceeded {
			t.Fatalf("%s state = %+v, want bucket block", m, st)
		}
		if d := time.Until(st.NextRetryAfter); d < 5*24*time.Hour-time.Minute || d > 5*24*time.Hour {
			t.Fatalf("%s blocked for %v, want until upstream reset", m, d)
		}
		if !strings.Contains(st.StatusMessage, "Gemini 额度耗尽") || !strings.Contains(st.StatusMessage, "重置 ") {
			t.Fatalf("%s message = %q", m, st.StatusMessage)
		}
	}
	if st := got.ModelStates["claude-sonnet-4-6"]; st != nil && st.Unavailable {
		t.Fatalf("claude must stay available, got %+v", st)
	}
	if got.Quota.Reason == "credential_quota" || got.LastError != nil && got.LastError.HTTPStatus == 401 {
		t.Fatalf("whole credential must not be blocked: %+v", got.Quota)
	}
	if pairs := coolingQuotaPairs([]*coreauth.Auth{got}, time.Now()); len(pairs) != 0 {
		t.Fatalf("bucket blocks must not be per-model probed: %+v", pairs)
	}

	// Recovery shown by the next probe plans an immediate release (the clear itself goes
	// through the cluster's atomic store, covered by ClearQuotaCooldown tests).
	verdicts := quotaBucketVerdicts([]QuotaBucketWindow{{ScopeID: "gemini", RemainingRatio: ratio(0.2)}}, time.Now())
	acts := planQuotaBucketActions(got, credentialBucketModels(got), verdicts, 5*time.Minute, true, time.Now())
	if len(acts) != 2 || acts[0].block || acts[1].block {
		t.Fatalf("recovery actions = %+v, want 2 releases", acts)
	}
}

func TestPlanQuotaBucketActionsCapsAtResetAndSkipsRewrite(t *testing.T) {
	now := time.Now().UTC()
	soon := now.Add(90 * time.Second)
	auth := &coreauth.Auth{ID: "a", ModelStates: map[string]*coreauth.ModelState{}}
	models := map[string][]string{quotaBucketGemini: {"gemini-2.5-flash"}}
	verdicts := map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, exhausted: true, resetAt: soon}}
	acts := planQuotaBucketActions(auth, models, verdicts, 5*time.Minute, true, now)
	if len(acts) != 1 || !acts[0].block || !acts[0].until.Equal(soon) {
		t.Fatalf("actions = %+v, want block until reset", acts)
	}
	auth.ModelStates["gemini-2.5-flash"] = &coreauth.ModelState{Unavailable: true, NextRetryAfter: soon,
		LastError: &coreauth.Error{Code: QuotaBucketExhaustedCode}}
	if acts := planQuotaBucketActions(auth, models, verdicts, 5*time.Minute, true, now); len(acts) != 0 {
		t.Fatalf("identical block rewritten: %+v", acts)
	}
	// Disabled: existing bucket blocks are released.
	if acts := planQuotaBucketActions(auth, models, verdicts, 5*time.Minute, false, now); len(acts) != 1 || acts[0].block {
		t.Fatalf("disabled actions = %+v, want release", acts)
	}
}

func TestPlanQuotaBucketActionsKeepsLongResetAndReleasesOnRecovery(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(123 * time.Hour)
	models := map[string][]string{quotaBucketGemini: {"gemini-3.8-flash-tiered"}}
	// A request-path 429 already cooled the model until the weekly reset.
	auth := &coreauth.Auth{ID: "a", ModelStates: map[string]*coreauth.ModelState{
		"gemini-3.8-flash-tiered": {Unavailable: true, NextRetryAfter: reset, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: reset},
			LastError: &coreauth.Error{Code: "", HTTPStatus: 429}},
	}}
	exhausted := map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, exhausted: true, resetAt: reset}}
	if acts := planQuotaBucketActions(auth, models, exhausted, 5*time.Minute, true, now); len(acts) != 0 {
		t.Fatalf("long cooldown must not be rewritten/shortened: %+v", acts)
	}
	noReset := map[string]quotaBucketVerdict{quotaBucketGemini: {known: true, exhausted: true}}
	if acts := planQuotaBucketActions(auth, models, noReset, 5*time.Minute, true, now); len(acts) != 0 {
		t.Fatalf("probe without reset must not shorten a longer cooldown: %+v", acts)
	}
	recovered := map[string]quotaBucketVerdict{quotaBucketGemini: {known: true}}
	acts := planQuotaBucketActions(auth, models, recovered, 5*time.Minute, true, now)
	if len(acts) != 1 || acts[0].block {
		t.Fatalf("recovery must release long upstream quota cooldown early: %+v", acts)
	}
	// Short (60s) 429 cooldowns are not touched by the guard.
	auth.ModelStates["gemini-3.8-flash-tiered"].NextRetryAfter = now.Add(60 * time.Second)
	if acts := planQuotaBucketActions(auth, models, recovered, 5*time.Minute, true, now); len(acts) != 0 {
		t.Fatalf("short cooldown must be left alone: %+v", acts)
	}
}

func TestCoolingQuotaPairsIncludesLongReset(t *testing.T) {
	now := time.Now()
	mk := func(d time.Duration) *coreauth.Auth {
		return &coreauth.Auth{ID: "x", Provider: "antigravity", ModelStates: map[string]*coreauth.ModelState{
			"m": {Unavailable: true, NextRetryAfter: now.Add(d), LastError: &coreauth.Error{HTTPStatus: 429}}}}
	}
	// Long resets are probed too (backoff-capped) to catch early/bonus resets.
	if n := len(coolingQuotaPairs([]*coreauth.Auth{mk(123 * time.Hour)}, now)); n != 1 {
		t.Fatalf("long cooldown not probed: %d", n)
	}
	if n := len(coolingQuotaPairs([]*coreauth.Auth{mk(time.Minute)}, now)); n != 1 {
		t.Fatalf("short cooldown not probed: %d", n)
	}
}
