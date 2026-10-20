package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuiltinArgonAlias_ListedOnlyWithHigh(t *testing.T) {
	ids := func(ms []*ModelInfo) map[string]bool {
		out := map[string]bool{}
		for _, m := range ms {
			out[m.ID] = true
		}
		return out
	}
	ultra := applyOAuthModelAliasForAuth(&config.Config{}, "antigravity", "", nil,
		[]*ModelInfo{{ID: "gemini-4-argon-high"}, {ID: "gemini-4-argon-low"}, {ID: "gemini-3-flash"}})
	got := ids(ultra)
	if !got["gemini-4-argon"] || !got["gemini-4-argon-high"] || !got["gemini-4-argon-low"] || !got["gemini-3-flash"] {
		t.Fatalf("ultra listing = %v", got)
	}
	pro := ids(applyOAuthModelAliasForAuth(nil, "antigravity", "", nil, []*ModelInfo{{ID: "gemini-3-flash"}}))
	if pro["gemini-4-argon"] || len(pro) != 1 {
		t.Fatalf("alias must not be listed without argon-high: %v", pro)
	}
}
