package home

import "testing"

func TestAntigravityDynamicThinking(t *testing.T) {
	f := false
	if th := antigravityDynamicThinking(antigravityUpstreamModel{ID: "gemini-2.5-flash-thinking"}); th == nil || !th.ZeroAllowed || th.Min <= 0 {
		t.Fatalf("2.5 flash family default: %+v", th)
	}
	if th := antigravityDynamicThinking(antigravityUpstreamModel{ID: "gemini-3.6-flash-high", MaxOutput: 65536}); th == nil || len(th.Levels) == 0 || th.Max != 65535 {
		t.Fatalf("gemini 3 default: %+v", th)
	}
	if th := antigravityDynamicThinking(antigravityUpstreamModel{ID: "gemini-2.5-flash", SupportsThinking: &f}); th != nil {
		t.Fatal("explicit supportsThinking=false must disable")
	}
	if th := antigravityDynamicThinking(antigravityUpstreamModel{ID: "gpt-oss-120b-medium"}); th != nil {
		t.Fatalf("non-gemini without hints: %+v", th)
	}
	ms := mergeAntigravityModels(nil, map[string]antigravityUpstreamModel{"gemini-2.5-flash-lite": {ID: "gemini-2.5-flash-lite"}}, nil, nil)
	if len(ms) != 1 || ms[0].Thinking == nil {
		t.Fatalf("merged dynamic model lacks thinking: %+v", ms)
	}
}
