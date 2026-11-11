package helps

import (
	"strings"
	"testing"
	"time"
)

func newTestErrCtx(n, maxBytes int, typ, fp, model string, ttl time.Duration) *errCtxCapturer {
	now := time.Now()
	return &errCtxCapturer{
		enabled: true, startedAt: now, ttl: ttl, deadline: now.Add(ttl),
		n: n, maxBytes: maxBytes, matchType: typ, fpFilter: fp, modelFilter: model,
		samples: make([]errCtxSample, 0, n),
	}
}

func TestErrCtxDisabledByDefault(t *testing.T) {
	c := &errCtxCapturer{}
	if c.active() {
		t.Fatal("zero-value capturer must be disabled")
	}
	c.record("full system prompt", "gemini-2.5-flash-lite", true, 0, 429,
		true, 200*time.Millisecond, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`)
	if c.snapshot()["enabled"].(bool) {
		t.Fatal("disabled snapshot must report enabled=false")
	}
}

func TestErrCtxCapturesTypeAndStopsAtN(t *testing.T) {
	c := newTestErrCtx(2, 1<<20, "429:quota", "", "", time.Hour)
	body := `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`
	for i := 0; i < 5; i++ {
		c.record("SECRET SYSTEM PROMPT CONTENT here", "gemini-2.5-flash", true, 3, 429, true, 150*time.Millisecond, body)
	}
	// a non-matching type must be ignored
	c.record("other", "gemini-2.5-flash", false, 0, 500, true, 10*time.Millisecond, "boom unknown")
	snap := c.snapshot()
	if snap["captured"].(int) != 2 {
		t.Fatalf("captured=%v (want 2, stop at N)", snap["captured"])
	}
	if !snap["stopped"].(bool) {
		t.Fatal("should be stopped after N")
	}
	s := snap["samples"].([]errCtxSample)
	if s[0].SysPrompt != "SECRET SYSTEM PROMPT CONTENT here" || s[0].ErrType != "429:quota" || s[0].Status != 429 {
		t.Fatalf("sample = %+v", s[0])
	}
}

func TestErrCtxFiltersAndTruncation(t *testing.T) {
	c := newTestErrCtx(10, 8, "429:quota", "Global chat", "gemini-2.5-flash-lite", time.Hour)
	body := `{"error":{"status":"RESOURCE_EXHAUSTED"}}`
	// wrong model -> skip
	c.record("Global chat rules here and a long tail", "gemini-3.8-flash-tiered", true, 0, 429, true, 100*time.Millisecond, body)
	// wrong fingerprint -> skip
	c.record("Different prefix entirely", "gemini-2.5-flash-lite", true, 0, 429, true, 100*time.Millisecond, body)
	// match + truncation to maxBytes=8
	c.record("Global chat rules (long content beyond cap)", "gemini-2.5-flash-lite", true, 0, 429, true, 100*time.Millisecond, body)
	snap := c.snapshot()
	if snap["captured"].(int) != 1 {
		t.Fatalf("captured=%v (want 1 after filters)", snap["captured"])
	}
	s := snap["samples"].([]errCtxSample)[0]
	if len(s.SysPrompt) != 8 || !s.Truncated || s.SysLen != len("Global chat rules (long content beyond cap)") {
		t.Fatalf("truncation wrong: %+v", s)
	}
}

func TestErrCtxTTLExpiryClears(t *testing.T) {
	c := newTestErrCtx(10, 1<<20, "429", "", "", time.Hour)
	c.record("x system prompt", "m", true, 0, 429, true, 10*time.Millisecond, "quota")
	c.deadline = time.Now().Add(-time.Minute)
	if c.active() {
		t.Fatal("expired must be inactive")
	}
	if c.samples != nil {
		t.Fatal("expired must clear samples")
	}
	if c.snapshot()["enabled"].(bool) {
		t.Fatal("expired snapshot enabled must be false")
	}
}

func TestClassifyErrType(t *testing.T) {
	cases := map[string]string{
		`{"error":{"status":"RESOURCE_EXHAUSTED"}}`:           "429:quota",
		`{"error":{"message":"Resource has been exhausted"}}`: "429:quota",
		`Unknown Error.`: "429:unknown",
	}
	for body, want := range cases {
		if got := classifyErrType(429, body); got != want {
			t.Errorf("classifyErrType(429,%q)=%q want %q", body, got, want)
		}
	}
	if classifyErrType(503, "no capacity") != "503" {
		t.Error("503 default")
	}
}

func TestSysSamplerInstant0AndRuleStats(t *testing.T) {
	s := newTestSampler(time.Hour)
	body := ""
	_ = body
	// instant (<1s) 429 with input_tokens==0
	s.recordFull("You are Codex, an agent based on GPT-5.", "m", true, 0, 429, true, 200*time.Millisecond, 0, "prompt_rewritten: codex-identity")
	// a 429 that is NOT instant0 (has tokens)
	s.recordFull("You are Codex, an agent based on GPT-5.", "m", true, 0, 429, true, 200*time.Millisecond, 5, "prompt_rewritten: codex-identity")
	// a rewritten request that recovered (200)
	s.recordFull("You are Codex, an agent based on GPT-5.", "m", true, 0, 200, false, 300*time.Millisecond, 7, "prompt_rewritten: codex-identity")
	snap := s.snapshot()
	if snap["total_429"].(int) != 2 || snap["instant_input0_429"].(int) != 1 {
		t.Fatalf("instant0 summary = %v / %v", snap["instant_input0_429"], snap["total_429"])
	}
	rs := snap["rule_stats"].(map[string]map[string]any)["codex-identity"]
	if rs["rewritten_total"].(int) != 3 || rs["still_429"].(int) != 2 || rs["ok"].(int) != 1 {
		t.Fatalf("rule_stats = %+v", rs)
	}
	if !strings.Contains(snap["fingerprints"].([]fpOut)[0].Prefix, "Codex") {
		t.Fatal("prefix missing")
	}
}
