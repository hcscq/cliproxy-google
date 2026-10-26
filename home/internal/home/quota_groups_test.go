package home

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

func tierAuth(id, tier string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive,
		Metadata: map[string]any{"subscription_tier": tier}, ModelStates: map[string]*coreauth.ModelState{}}
}

func TestQuotaGroupOverridesDefaults(t *testing.T) {
	rules, errs := parseQuotaGroupOverrides("")
	if len(errs) != 0 || len(rules) != 4 {
		t.Fatalf("rules=%+v errs=%v", rules, errs)
	}
	cases := []struct{ tier, model, want string }{
		{"pro", "gemini-4-argon-high", "argon"},
		{"pro", "gemini-4-argon", "argon"},
		{"ultra", "gemini-4-argon-high", "gemini"},
		{"pro", "gemini-3.8-flash", "gemini"},
		{"free", "claude-opus-4-6-thinking", "3p"},
		{"ultra", "models/gpt-oss-120b-medium", "3p"},
		{"pro", "chat_20706", ""},
		{"unknown", "gemini-4-argon-high", "argon"},
	}
	for _, c := range cases {
		if got := matchQuotaGroupRules(rules, c.tier, c.model); got != c.want {
			t.Errorf("%s %s = %q want %q", c.tier, c.model, got, c.want)
		}
	}
	rules, errs = parseQuotaGroupOverrides("pro,free: a-*|b=x\nbad rule\n*: [=y")
	if len(rules) != 1 || len(errs) != 3 || matchQuotaGroupRules(rules, "free", "a-1") != "x" || matchQuotaGroupRules(rules, "ultra", "a-1") != "" {
		t.Fatalf("rules=%+v errs=%v", rules, errs)
	}
	if r, _ := parseQuotaGroupOverrides("off"); r != nil {
		t.Fatalf("off must disable overrides")
	}
}

func f(v float64) *float64 { return &v }

func TestLearnQuotaGroupsBySignature(t *testing.T) {
	h := &antigravityCatalogHealth{models: map[string]CatalogModelHealth{
		"gemini-2.5-flash":  {QuotaRemaining: f(0.6), QuotaResetTime: "2026-10-30T01:00:00Z"},
		"gemini-3.8-flash":  {QuotaRemaining: f(0.6), QuotaResetTime: "2026-10-30T01:00:00Z"},
		"gemini-4-argon":    {QuotaRemaining: f(0), QuotaResetTime: "2026-10-31T01:00:00Z"},
		"claude-sonnet-4-6": {QuotaRemaining: f(1)},
		"chat_1":            {QuotaRemaining: f(0.6), QuotaResetTime: "2026-10-30T01:00:00Z"},
	}}
	g := learnQuotaGroups(h)
	if g["gemini-2.5-flash"] == "" || g["gemini-2.5-flash"] != g["gemini-3.8-flash"] || g["gemini-4-argon"] == g["gemini-2.5-flash"] {
		t.Fatalf("learned=%v", g)
	}
	if _, ok := g["claude-sonnet-4-6"]; ok {
		t.Fatalf("full signature must be skipped")
	}
	if _, ok := g["chat_1"]; ok {
		t.Fatalf("internal models ignored")
	}
	// Learned groups are used when no override matches.
	quotaGroupRulesCur.Store(&quotaGroupRuleSet{raw: "x", rules: nil})
	t.Cleanup(func() { quotaGroupRulesCur.Store(nil) })
	a := tierAuth("learn-a", "g1-pro-tier")
	learnedQuotaGroupsByAuth.Store(a.ID, g)
	t.Cleanup(func() { learnedQuotaGroupsByAuth.Delete(a.ID) })
	if grp, src := QuotaGroupFor(a, "gemini-3.8-flash"); grp != g["gemini-2.5-flash"] || src != "learned" {
		t.Fatalf("got %q %q", grp, src)
	}
	if grp, _ := QuotaGroupFor(a, "unknown-model"); grp != "" {
		t.Fatalf("unknown model must be per-model")
	}
}

func TestQuotaTypeUntil(t *testing.T) {
	now := time.Now()
	reset := now.Add(97 * time.Hour)
	quota := coreauth.Result{Model: "m", RetryAfter: nil, ResetAt: &reset, Error: &coreauth.Error{HTTPStatus: 429, Code: coreauth.ModelQuotaExhaustedCode}}
	if u, ok := quotaTypeUntil(quota, `QUOTA_EXHAUSTED`, now); !ok || !u.Equal(reset.UTC()) {
		t.Fatalf("quota 429 not detected: %v %v", u, ok)
	}
	text := coreauth.Result{Model: "m", Error: &coreauth.Error{HTTPStatus: 429}}
	if u, ok := quotaTypeUntil(text, `You have exhausted your quota. Resets in 117h.`, now); !ok || u.Sub(now) < 116*time.Hour {
		t.Fatalf("resets-in not parsed: %v %v", u, ok)
	}
	capacity := coreauth.Result{Model: "m", ResetAt: &reset, Error: &coreauth.Error{HTTPStatus: 429, Code: coreauth.ModelQuotaExhaustedCode}}
	if _, ok := quotaTypeUntil(capacity, `You have exhausted your capacity on this model.`, now); ok {
		t.Fatalf("capacity 429 must stay per-model")
	}
	short := coreauth.Result{Model: "m", Error: &coreauth.Error{HTTPStatus: 429}}
	if _, ok := quotaTypeUntil(short, `RESOURCE_EXHAUSTED`, now); ok {
		t.Fatalf("short rate limit must stay per-model")
	}
}

func TestPropagateQuotaGroupCooldown(t *testing.T) {
	quotaGroupRulesCur.Store(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	for _, a := range []*coreauth.Auth{tierAuth("grp-pro", "g1-pro-tier"), tierAuth("grp-ultra", "g1-ultra-tier")} {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "antigravity", []*registry.ModelInfo{
			{ID: "gemini-4-argon"}, {ID: "gemini-4-argon-high"}, {ID: "gemini-3.8-flash"}, {ID: "claude-sonnet-4-6"},
		})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	rt := &Runtime{coreManager: manager}
	reset := time.Now().Add(90 * time.Hour)
	res := coreauth.Result{AuthID: "grp-pro", Provider: "antigravity", Model: "gemini-4-argon-high", ResetAt: &reset,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Code: coreauth.ModelQuotaExhaustedCode}}
	if n := rt.propagateQuotaGroupCooldown(context.Background(), "grp-pro", res, "QUOTA_EXHAUSTED"); n != 1 {
		t.Fatalf("pro: propagated %d, want 1 (argon sibling only)", n)
	}
	got, _ := manager.GetByID("grp-pro")
	if st := got.ModelStates["gemini-4-argon"]; st == nil || !st.Unavailable || st.LastError.Code != QuotaGroupExhaustedCode || !strings.Contains(st.StatusMessage, "argon") {
		t.Fatalf("argon sibling state %+v", st)
	}
	if st := got.ModelStates["gemini-3.8-flash"]; st != nil && st.Unavailable {
		t.Fatalf("flash must stay available on pro")
	}
	if pairs := coolingQuotaPairs([]*coreauth.Auth{got}, time.Now()); len(pairs) != 0 {
		t.Fatalf("propagated siblings must not be probed: %+v", pairs)
	}
	res.AuthID = "grp-ultra"
	if n := rt.propagateQuotaGroupCooldown(context.Background(), "grp-ultra", res, "QUOTA_EXHAUSTED"); n != 2 {
		t.Fatalf("ultra: propagated %d, want 2 (argon + flash)", n)
	}
	// Capacity 429s never propagate.
	res.AuthID = "grp-pro"
	res.Model = "gemini-3.8-flash"
	if n := rt.propagateQuotaGroupCooldown(context.Background(), "grp-pro", res, "exhausted your capacity on this model"); n != 0 {
		t.Fatalf("capacity propagated %d", n)
	}
	// Bucket guard no longer treats pro argon as part of the Gemini bucket.
	b := credentialBucketModels(got)
	for _, m := range b[quotaBucketGemini] {
		if strings.Contains(m, "argon") {
			t.Fatalf("pro argon in gemini bucket: %v", b)
		}
	}
}

func TestFilterQuotaProbePairs(t *testing.T) {
	quotaGroupRulesCur.Store(nil)
	now := time.Now()
	a := tierAuth("probe-a", "g1-ultra-tier")
	pairs := []quotaProbeTarget{
		{auth: a, model: "gemini-3.8-flash", reset: now.Add(2 * time.Minute)},
		{auth: a, model: "gemini-2.5-flash", reset: now.Add(3 * time.Minute)},
		{auth: a, model: "claude-sonnet-4-6", reset: now.Add(5 * time.Minute)},
		{auth: a, model: "gemini-4-argon", reset: now.Add(90 * time.Hour)},
	}
	out := filterQuotaProbePairs(pairs, nil, 30*time.Minute, now)
	if len(out) != 2 || out[0].model != "gemini-3.8-flash" || out[1].model != "claude-sonnet-4-6" {
		t.Fatalf("got %+v", out)
	}
	// Nudged far pairs are probed once; threshold 0 disables the skip.
	out = filterQuotaProbePairs(pairs[3:], map[string]bool{"probe-a|gemini-4-argon": true}, 30*time.Minute, now)
	if len(out) != 1 {
		t.Fatalf("nudged pair skipped")
	}
	if out = filterQuotaProbePairs(pairs[3:], nil, 0, now); len(out) != 1 {
		t.Fatalf("skip disabled must keep pair")
	}
}

func TestLowHealthyCounts(t *testing.T) {
	updateLowHealthy("lh-a", map[string]bool{"lh-m": true})
	updateLowHealthy("lh-b", map[string]bool{"lh-m": true})
	updateLowHealthy("lh-a", map[string]bool{})
	if n := lowHealthyCount("lh-m"); n != 1 {
		t.Fatalf("count=%d", n)
	}
}

func TestQuotaTierCacheWithoutMetadata(t *testing.T) {
	quotaGroupRulesCur.Store(nil)
	a := &coreauth.Auth{ID: "tier-cache-a", Provider: "antigravity"}
	if g, _ := QuotaGroupFor(a, "gemini-4-argon-high"); g != "argon" {
		t.Fatalf("unknown tier must keep argon separate, got %q", g)
	}
	setQuotaTier(a.ID, "g1-ultra-tier")
	t.Cleanup(func() { quotaTierCache.Delete(a.ID) })
	if g, _ := QuotaGroupFor(a, "gemini-4-argon-high"); g != "gemini" {
		t.Fatalf("cached ultra tier: got %q", g)
	}
}
