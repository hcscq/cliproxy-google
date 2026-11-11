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

func TestApplyAntigravityPromptRewritesCodexIdentity(t *testing.T) {
	const codex = "You are Codex, an agent based on GPT-5."
	const want = "You are Co\u034Fdex, an agent based on GPT-5."
	in := []byte(`{"request":{"systemInstruction":{"role":"user","parts":[{"text":"You are a helpful coding assistant."},{"text":"Intro. ` + codex + ` You are running as a coding agent."}]},"contents":[{"role":"user","parts":[{"text":"` + codex + `"}]}]}}`)
	out, hits, detect := applyAntigravityPromptRewrites(in, antigravityPromptRewriteRules(nil))
	if len(hits) != 1 || hits[0] != "codex-identity" || len(detect) != 0 {
		t.Fatalf("hits=%v detect=%v", hits, detect)
	}
	if got := gjson.GetBytes(out, "request.systemInstruction.parts.1.text").String(); got != "Intro. "+want+" You are running as a coding agent." {
		t.Fatalf("rewritten = %q", got)
	}
	if strings.Contains(gjson.GetBytes(out, "request.systemInstruction.parts.1.text").String(), "\u200b") {
		t.Fatal("must not use U+200B (stripped upstream, still 429)")
	}
	if gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String() != "You are a helpful coding assistant." {
		t.Fatal("unrelated system part changed")
	}
	if gjson.GetBytes(out, "request.systemInstruction.role").String() != "user" {
		t.Fatal("systemInstruction role lost")
	}
	if gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != codex {
		t.Fatal("user content must not be rewritten")
	}
	// snake_case variant is covered too
	in2 := []byte(`{"request":{"system_instruction":{"parts":[{"text":"` + codex + `"}]}}}`)
	out2, hits2, _ := applyAntigravityPromptRewrites(in2, antigravityPromptRewriteRules(nil))
	if len(hits2) != 1 || gjson.GetBytes(out2, "request.system_instruction.parts.0.text").String() != want {
		t.Fatalf("snake_case: hits=%v out=%s", hits2, out2)
	}
}

func TestApplyAntigravityPromptRewritesCodexNearMissesUntouched(t *testing.T) {
	for _, text := range []string{
		"You are Codex.",
		"You are an agent based on GPT-5.",
		"You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI.",
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are Co\u034Fdex, an agent based on GPT-5.",
	} {
		in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"` + text + `"}]}}}`)
		out, hits, _ := applyAntigravityPromptRewrites(in, antigravityPromptRewriteRules(nil))
		if len(hits) != 0 || string(out) != string(in) {
			t.Errorf("%q: hits=%v out=%s", text, hits, out)
		}
	}
}
