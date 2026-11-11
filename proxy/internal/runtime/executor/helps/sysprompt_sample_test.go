package helps

import (
	"fmt"
	"testing"
	"time"
)

func newTestSampler(ttl time.Duration) *sysPromptSampler {
	now := time.Now()
	return &sysPromptSampler{
		enabled:   true,
		startedAt: now,
		ttl:       ttl,
		deadline:  now.Add(ttl),
		fps:       map[string]*sysSampleEntry{},
		cands:     map[string]*sysSampleCand{},
		rules:     map[string]*ruleStat{},
	}
}

func TestSysSamplerDisabledByDefault(t *testing.T) {
	s := &sysPromptSampler{}
	if s.active() {
		t.Fatal("zero-value sampler must be disabled")
	}
	s.record("You are Codex, an agent based on GPT-5.", "gemini-2.5-flash-lite", true, 3, 429, true)
	snap := s.snapshot()
	if snap["enabled"].(bool) {
		t.Fatal("disabled snapshot must report enabled=false")
	}
	if _, ok := snap["fingerprints"]; ok {
		t.Fatal("disabled sampler must not expose fingerprints")
	}
}

func TestSysSamplerAggregatesAndCandidates(t *testing.T) {
	s := newTestSampler(time.Hour)
	codex := "You are Codex,  an agent\n based on GPT-5."
	for i := 0; i < 5; i++ {
		s.recordWithLatency(codex, "gemini-2.5-flash-lite", true, 2, 429, true, 150*time.Millisecond)
	}
	s.recordWithLatency(codex, "gemini-2.5-flash", true, 2, 200, false, 0)
	other := "You are a helpful coding assistant from Anthropic Claude Code."
	s.recordWithLatency(other, "gemini-2.5-flash", false, 0, 200, false, 0)

	snap := s.snapshot()
	if snap["total_requests"].(int) != 7 {
		t.Fatalf("total=%v", snap["total_requests"])
	}
	fps := snap["fingerprints"].([]fpOut)
	if len(fps) != 2 {
		t.Fatalf("want 2 fingerprints, got %d", len(fps))
	}
	top := fps[0] // highest c429 first
	if top.C429 != 5 || top.CSuccess != 1 || top.Total != 6 {
		t.Fatalf("top entry counts = %+v", top)
	}
	if top.Rate429Pct < 83.3 || top.Rate429Pct > 83.4 {
		t.Fatalf("rate_429 = %v", top.Rate429Pct)
	}
	if top.Avg429Millis != 150 {
		t.Fatalf("avg429 = %d", top.Avg429Millis)
	}
	// whitespace folding: no newline / double space in prefix
	if gotNL := containsAny(top.Prefix, "\n"); gotNL {
		t.Fatalf("prefix not folded: %q", top.Prefix)
	}
	cands := snap["candidates"].(map[string]map[string]int)
	if cands["Codex"]["contains"] != 6 || cands["Codex"]["c429"] != 5 || cands["Codex"]["c_success"] != 1 {
		t.Fatalf("Codex candidate = %+v", cands["Codex"])
	}
	if cands["GPT-5"]["contains"] != 6 {
		t.Fatalf("GPT-5 candidate = %+v", cands["GPT-5"])
	}
	if cands["Anthropic"]["contains"] != 1 || cands["Claude Code"]["contains"] != 1 {
		t.Fatalf("anthropic/claude candidate = %+v %+v", cands["Anthropic"], cands["Claude Code"])
	}
}

func TestSysSamplerOverflowToOther(t *testing.T) {
	s := newTestSampler(time.Hour)
	for i := 0; i < sysSampleMaxFinger+10; i++ {
		s.record(fmt.Sprintf("distinct system prompt number %d", i), "m", true, 0, 429, true)
	}
	if len(s.fps) != sysSampleMaxFinger {
		t.Fatalf("fps capped at %d, got %d", sysSampleMaxFinger, len(s.fps))
	}
	if s.other == nil || s.other.Total != 10 {
		t.Fatalf("overflow other = %+v", s.other)
	}
}

func TestSysSamplerTTLExpiry(t *testing.T) {
	s := newTestSampler(time.Hour)
	s.record("You are Codex, an agent based on GPT-5.", "m", true, 0, 429, true)
	// force past deadline
	s.deadline = time.Now().Add(-time.Minute)
	if s.active() {
		t.Fatal("expired sampler must be inactive")
	}
	if s.fps != nil || s.cands != nil {
		t.Fatal("expired sampler must drop retained data")
	}
	snap := s.snapshot()
	if snap["enabled"].(bool) {
		t.Fatal("expired snapshot enabled must be false")
	}
	// recording after expiry is a no-op
	s.record("x", "m", true, 0, 429, true)
	if s.fps != nil {
		t.Fatal("must not resurrect after expiry")
	}
}

func containsAny(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
