package home

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

func TestExtraModelInheritsCatalogSibling(t *testing.T) {
	upstream := map[string]antigravityUpstreamModel{
		"gemini-3.8-flash-tiered": {ID: "gemini-3.8-flash-tiered", MaxTokens: 1048576, MaxOutput: 65536},
	}
	out := mergeAntigravityModels(nil, upstream, nil, []string{"gemini-3.8-flash-high", "no-family"})
	byID := map[string]*ModelInfo{}
	for _, m := range out {
		byID[m.ID] = m
	}
	sib, hi, plain := byID["gemini-3.8-flash-tiered"], byID["gemini-3.8-flash-high"], byID["no-family"]
	if hi == nil || plain == nil || sib == nil {
		t.Fatalf("missing models: %v", out)
	}
	if hi.ID != "gemini-3.8-flash-high" || hi.Description != antigravityExtraNote || hi.UserDefined {
		t.Fatalf("extra entry: %+v", hi)
	}
	if hi.InputTokenLimit != 1048576 || hi.OutputTokenLimit != 65536 {
		t.Fatalf("limits not inherited: %+v", hi)
	}
	if (sib.Thinking == nil) != (hi.Thinking == nil) {
		t.Fatalf("thinking not inherited: sib=%v hi=%v", sib.Thinking, hi.Thinking)
	}
	if hi.Thinking != nil && sib.Thinking != nil && &hi.Thinking.Levels == &sib.Thinking.Levels {
		t.Fatal("thinking shares state with sibling")
	}
	if !plain.UserDefined || plain.Description != antigravityExtraNote {
		t.Fatalf("no-sibling extra should pass thinking through: %+v", plain)
	}
	_ = registry.ThinkingSupport{}
}
