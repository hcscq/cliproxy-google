package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

func TestBuiltinAlias_GeminiArgonResolvesToHigh(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(nil)
	auth := &Auth{Provider: "antigravity", Attributes: map[string]string{"auth_kind": "oauth"}}
	if got := mgr.resolveOAuthUpstreamModel(auth, "gemini-4-argon"); got != "gemini-4-argon-high" {
		t.Fatalf("gemini-4-argon -> %q, want gemini-4-argon-high", got)
	}
	if got := mgr.resolveOAuthUpstreamModel(auth, "gemini-4-argon-low"); got != "" && got != "gemini-4-argon-low" {
		t.Fatalf("gemini-4-argon-low must not be rewritten, got %q", got)
	}
	if got := mgr.resolveOAuthUpstreamModel(&Auth{Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}, "gemini-4-argon"); got == "gemini-4-argon-high" {
		t.Fatalf("builtin alias must be antigravity-only")
	}
}

func TestBuiltinAlias_ConfiguredAliasWins(t *testing.T) {
	in := map[string][]internalconfig.OAuthModelAlias{
		"Antigravity": {{Name: "gemini-4-argon-medium", Alias: "gemini-4-argon"}},
		"claude":      {{Name: "x", Alias: "y"}},
	}
	out := WithBuiltinOAuthModelAliases(in)
	if len(out["Antigravity"]) != 1 || out["Antigravity"][0].Name != "gemini-4-argon-medium" {
		t.Fatalf("configured alias must win, got %+v", out["Antigravity"])
	}
	if _, dup := out["antigravity"]; dup {
		t.Fatalf("channel matched case-insensitively; must not add a second key")
	}
	if len(in["Antigravity"]) != 1 || len(out["claude"]) != 1 {
		t.Fatalf("input mutated or other channels changed")
	}
	if got := WithBuiltinOAuthModelAliases(nil)["antigravity"]; len(got) != 1 || !got[0].Fork {
		t.Fatalf("builtin must be present as fork alias, got %+v", got)
	}
}
