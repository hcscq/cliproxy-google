package handlers

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResolveEffortVariantModel(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"antigravity": {
		{Name: "claude-opus-5-5-high", Alias: "claude-opus-5-5", Fork: true},
		{Name: "claude-opus-4-6-thinking", Alias: "claude-opus-4-6", Fork: true},
	}})
	h := &BaseAPIHandler{AuthManager: m}
	cases := []struct{ proto, model, body, want string }{
		{"openai", "claude-opus-5-5", `{"reasoning_effort":"medium"}`, "claude-opus-5-5-medium"},
		{"openai", "claude-opus-5-5", `{"reasoning_effort":"low"}`, "claude-opus-5-5-low"},
		{"openai", "claude-opus-5-5", `{"reasoning_effort":"xhigh"}`, "claude-opus-5-5"},
		{"openai", "claude-opus-5-5", `{}`, "claude-opus-5-5"},
		{"openai", "claude-opus-5-5(low)", `{}`, "claude-opus-5-5-low"},
		{"claude", "claude-opus-5-5", `{"output_config":{"effort":"medium"}}`, "claude-opus-5-5-medium"},
		{"claude", "claude-opus-5-5", `{"thinking":{"type":"enabled","budget_tokens":2048}}`, "claude-opus-5-5-medium"},
		{"claude", "claude-opus-5-5", `{"thinking":{"type":"adaptive"}}`, "claude-opus-5-5"},
		{"openai", "claude-opus-4-6", `{"reasoning_effort":"low"}`, "claude-opus-4-6"},
		{"openai", "claude-opus-5-5-high", `{"reasoning_effort":"low"}`, "claude-opus-5-5-high"},
		{"openai", "gemini-3.8-flash", `{"reasoning_effort":"low"}`, "gemini-3.8-flash"},
	}
	for _, c := range cases {
		if got := h.resolveEffortVariantModel(c.proto, c.model, []byte(c.body)); got != c.want {
			t.Errorf("%s %s %s: got %s want %s", c.proto, c.model, c.body, got, c.want)
		}
	}
}

func TestResolveEffortVariantModelLearnedFromHome(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	m.LearnHomeAliasForTest("claude-sonnet-5-5", "claude-sonnet-5-5-high")
	h := &BaseAPIHandler{AuthManager: m}
	if got := h.resolveEffortVariantModel("openai", "claude-sonnet-5-5", []byte(`{"reasoning_effort":"low"}`)); got != "claude-sonnet-5-5-low" {
		t.Fatalf("got %s", got)
	}
}
