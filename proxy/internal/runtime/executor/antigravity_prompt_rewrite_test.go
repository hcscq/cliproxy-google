package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyAntigravityPromptRewritesDefaultIdentity(t *testing.T) {
	in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"x-anthropic-billing-header: a"},{"text":"You are a Claude agent, built on Anthropic's Claude Agent SDK."},{"text":"Keep me"}]},"contents":[{"role":"user","parts":[{"text":"You are a Claude agent, built on Anthropic's Claude Agent SDK."}]}]}}`)
	out, hits, detect := applyAntigravityPromptRewrites(in, antigravityPromptRewriteRules(nil))
	if len(hits) != 2 || hits[0] != "claude-code-billing-header" || hits[1] != "claude-agent-sdk-identity" || len(detect) != 0 {
		t.Fatalf("hits=%v detect=%v", hits, detect)
	}
	if got := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String(); got != "You are an AI coding agent operating inside a CLI agent harness." {
		t.Fatalf("rewritten = %q", got)
	}
	if gjson.GetBytes(out, "request.systemInstruction.parts.1.text").String() != "Keep me" {
		t.Fatal("other system part changed")
	}
	if !strings.Contains(gjson.GetBytes(out, "request.contents.0.parts.0.text").String(), "Claude Agent SDK") {
		t.Fatal("user content must not be rewritten")
	}
}

func TestApplyAntigravityPromptRewritesConfigurable(t *testing.T) {
	cfg := &config.Config{}
	empty := []config.PromptRewriteRule{}
	cfg.Antigravity.PromptRewrites = &empty
	in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"You are a Claude agent, built on Anthropic's Claude Agent SDK. FOO"}]}}}`)
	if out, hits, _ := applyAntigravityPromptRewrites(in, antigravityPromptRewriteRules(cfg)); len(hits) != 0 || string(out) != string(in) {
		t.Fatal("explicit empty list must disable rewriting")
	}
	rules := []config.PromptRewriteRule{{Name: "foo", Match: "FOO"}, {Name: "off", Match: "You are", Replace: "x", Disabled: true}}
	out, hits, detect := applyAntigravityPromptRewrites(in, rules)
	if string(out) != string(in) || len(hits) != 1 || len(detect) != 1 || detect[0] != "foo" {
		t.Fatalf("detect-only: out=%s hits=%v detect=%v", out, hits, detect)
	}
}

func TestIsAntigravityBare429(t *testing.T) {
	cases := map[string]bool{
		`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`:                                                                       true,
		`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 5m3s.","status":"RESOURCE_EXHAUSTED"}}`:                                     false,
		`{"error":{"code":429,"message":"Individual quota reached.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`: false,
		`not json`: false,
	}
	for body, want := range cases {
		if got := isAntigravityBare429([]byte(body)); got != want {
			t.Errorf("isAntigravityBare429(%s) = %v, want %v", body, got, want)
		}
	}
	if err := antigravityPromptBlockErr([]string{"foo"}); err.code != 400 || !gjson.Valid(err.msg) {
		t.Fatalf("block err = %+v", err)
	}
}

func TestApplyAntigravityPromptRewritesRemoveLineInline(t *testing.T) {
	in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"x-anthropic-billing-header: cc_version=1;\nYou are a Claude agent, built on Anthropic's Claude Agent SDK.\nRules"}]}}}`)
	out, hits, _ := applyAntigravityPromptRewrites(in, antigravityPromptRewriteRules(nil))
	got := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String()
	if len(hits) != 2 || got != "You are an AI coding agent operating inside a CLI agent harness.\nRules" {
		t.Fatalf("hits=%v got=%q", hits, got)
	}
	only := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"x-anthropic-billing-header: a"}]},"contents":[]}}`)
	if out, _, _ := applyAntigravityPromptRewrites(only, antigravityPromptRewriteRules(nil)); gjson.GetBytes(out, "request.systemInstruction").Exists() {
		t.Fatalf("empty system instruction should be dropped: %s", out)
	}
}
