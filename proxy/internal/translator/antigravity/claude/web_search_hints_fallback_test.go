package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// Production registry had no model flagged SupportsWebSearch (hints missing):
// Claude Code's WebSearch sub-request must still run native googleSearch on a
// Gemini text model instead of dropping the tool.
func TestClaudeWebSearchFallsBackWhenNoCapabilityHints(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("test-ws-no-hints", "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3.8-flash-tiered"}, {ID: "gemini-3.1-flash-image"}, {ID: "claude-sonnet-4-6"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("test-ws-no-hints") })
	in := []byte(`{"messages":[{"role":"user","content":"Perform a web search for the query: x"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`)

	out, _ := ConvertClaudeRequestToAntigravity("gemini-3.8-flash-tiered", in, true)
	if gjson.GetBytes(out, "requestType").String() != "web_search" || gjson.GetBytes(out, "request.tools.0.googleSearch").Raw == "" {
		t.Fatalf("want native web_search request: %s", out)
	}
	if gjson.GetBytes(out, "model").String() != "gemini-3.8-flash-tiered" {
		t.Fatalf("model must not change: %s", out)
	}
	for _, m := range []string{"gemini-3.1-flash-image", "claude-sonnet-4-6"} {
		out, _ = ConvertClaudeRequestToAntigravity(m, in, true)
		if gjson.GetBytes(out, "requestType").String() == "web_search" {
			t.Fatalf("%s must not use the fallback: %s", m, out)
		}
	}
}

func TestClaudeWebSearchFallbackHonoursCatalogVeto(t *testing.T) {
	no := false
	registry.GetGlobalRegistry().RegisterClient("test-ws-veto", "antigravity", []*registry.ModelInfo{
		{ID: "gemini-2.5-flash", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &no}},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("test-ws-veto") })
	in := []byte(`{"messages":[{"role":"user","content":"q"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	out, _ := ConvertClaudeRequestToAntigravity("gemini-2.5-flash", in, true)
	if gjson.GetBytes(out, "requestType").String() == "web_search" {
		t.Fatalf("catalog veto must win: %s", out)
	}
}
