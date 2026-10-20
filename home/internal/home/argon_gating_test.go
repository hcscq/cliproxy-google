package home

import "testing"

func TestMergeAntigravityModels_GatedStaticNeedsCatalog(t *testing.T) {
	static := []*ModelInfo{{ID: "gemini-4-argon-high"}, {ID: "gemini-3-flash"}}
	has := func(ms []*ModelInfo, id string) bool {
		for _, m := range ms {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	cold := mergeAntigravityModels(static, nil, nil, nil)
	if has(cold, "gemini-4-argon-high") || !has(cold, "gemini-3-flash") {
		t.Fatalf("cold cache: gated model must be skipped, others kept: %v", cold)
	}
	pro := mergeAntigravityModels(static, map[string]antigravityUpstreamModel{"gemini-3-flash": {ID: "gemini-3-flash"}}, nil, nil)
	if has(pro, "gemini-4-argon-high") {
		t.Fatal("pro catalog without argon must not get it")
	}
	ultra := mergeAntigravityModels(static, map[string]antigravityUpstreamModel{"gemini-4-argon-high": {ID: "gemini-4-argon-high"}, "gemini-3-flash": {ID: "gemini-3-flash"}}, nil, nil)
	if !has(ultra, "gemini-4-argon-high") {
		t.Fatal("ultra catalog must keep argon-high")
	}
}

func TestApplyOAuthModelAlias_BuiltinArgonOnlyWithHigh(t *testing.T) {
	ultra := applyOAuthModelAlias(nil, "antigravity", "", []*ModelInfo{{ID: "gemini-4-argon-high"}, {ID: "gemini-3-flash"}})
	ids := map[string]bool{}
	for _, m := range ultra {
		ids[m.ID] = true
	}
	if !ids["gemini-4-argon"] || !ids["gemini-4-argon-high"] {
		t.Fatalf("ultra = %v", ids)
	}
	pro := applyOAuthModelAlias(nil, "antigravity", "", []*ModelInfo{{ID: "gemini-3-flash"}})
	if len(pro) != 1 {
		t.Fatalf("pro must not get alias: %v", pro)
	}
}
