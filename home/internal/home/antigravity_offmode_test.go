package home

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

func TestAntigravityThinkingOffModePerModel(t *testing.T) {
	yes := true
	want := map[string]string{
		"gemini-3.1-flash-lite":   "zero",
		"gemini-3.5-flash-low":    "zero",
		"gemini-3.5-flash-lite":   "omit",
		"gemini-3-flash":          "minimal",
		"gemini-3.6-flash-high":   "zero-reduced",
		"gemini-3.8-flash-tiered": "zero-reduced",
		"gemini-3.1-pro-high":     "", // not measurable (rejects default requests): generic fallback
	}
	for id, mode := range want {
		ts := antigravityDynamicThinking(antigravityUpstreamModel{ID: id, SupportsThinking: &yes})
		if ts == nil {
			t.Fatalf("%s: no thinking support", id)
		}
		if ts.OffMode != mode {
			t.Errorf("%s: off mode %q, want %q", id, ts.OffMode, mode)
		}
		raw, _ := json.Marshal(ts)
		if mode != "" && !strings.Contains(string(raw), `"off_mode":"`+mode+`"`) {
			t.Errorf("%s: off_mode missing from the JSON sent to proxies: %s", id, raw)
		}
	}
}

func TestAntigravityStaticModelsGetOffModeWithoutMutation(t *testing.T) {
	static := &ModelInfo{ID: "gemini-3.1-flash-lite", Thinking: &registry.ThinkingSupport{Min: 1, Max: 65535, ZeroAllowed: true, Levels: []string{"minimal", "low"}}}
	upstream := map[string]antigravityUpstreamModel{"gemini-3.1-flash-lite": {ID: "gemini-3.1-flash-lite"}}
	out := mergeAntigravityModels([]*ModelInfo{static}, upstream, nil, nil)
	if len(out) != 1 || out[0].Thinking == nil || out[0].Thinking.OffMode != "zero" {
		t.Fatalf("static model off mode not attached: %+v", out)
	}
	if static.Thinking.OffMode != "" {
		t.Fatal("shared static definition was mutated")
	}
}
