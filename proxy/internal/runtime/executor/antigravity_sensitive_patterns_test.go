package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// prodCodexPattern is the production rule (upstream #7545 guidance, widened to
// also cover the older "an agent" banner).
var prodCodexPattern = config.SensitivePatternRule{
	Pattern:     `(?i)You are Codex,\s+(?:an?\s+)?(?:coding\s+)?agent\s+based\s+on\s+[^.\r\n"]+\.?`,
	Replacement: "You are an AI coding assistant.",
}

func TestSensitivePatternsCodexBanners(t *testing.T) {
	compiled := compileSensitivePatterns([]config.SensitivePatternRule{prodCodexPattern})
	cases := map[string]string{
		"You are Codex, a coding agent based on GPT-5. Rest.": "You are an AI coding assistant. Rest.",
		"You are Codex, an agent based on GPT-5.\nRest":       "You are an AI coding assistant.\nRest",
		"Intro\nyou are codex, a coding agent based on GPT-5":  "Intro\nYou are an AI coding assistant.",
		"You are Codex.":                                      "You are Codex.",
	}
	for in, want := range cases {
		got, _ := applySensitivePatternsToText(in, compiled)
		if got != want {
			t.Errorf("in=%q got=%q want=%q", in, got, want)
		}
	}
}

func TestSensitivePatternsNonCascadingOrderedAndDelete(t *testing.T) {
	rules := compileSensitivePatterns([]config.SensitivePatternRule{
		{Pattern: `foo`, Replacement: "bar"},
		{Pattern: `bar`, Replacement: "baz"}, // must not see the "bar" produced by rule 1
		{Pattern: `(?m)^DROP[^\n]*\n?`, Replacement: ""},
		{Pattern: `fo`, Replacement: "XX"}, // overlaps rule 1 spans -> skipped
		{Pattern: `x*`, Replacement: "!"},  // empty matches ignored
		{Pattern: `(\w+)@host`, Replacement: "$1@redacted"},
	})
	got, n := applySensitivePatternsToText("foo bar\nDROP this line\nkeep u@host", rules)
	want := "bar baz\nkeep u@redacted"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if n != 4 {
		t.Fatalf("n=%d want 4", n)
	}
}

func TestApplyAntigravitySensitivePatternsSystemOnly(t *testing.T) {
	in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"You are Codex, a coding agent based on GPT-5."},{"text":"DROPME"},{"inlineData":{"x":1}}]},"contents":[{"role":"user","parts":[{"text":"You are Codex, a coding agent based on GPT-5."}]}]}}`)
	rules := []config.SensitivePatternRule{prodCodexPattern, {Pattern: `^DROPME$`, Replacement: ""}}
	out, n := applyAntigravitySensitivePatterns(in, rules)
	if n != 2 {
		t.Fatalf("n=%d", n)
	}
	parts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "You are an AI coding assistant." || !parts[1].Get("inlineData").Exists() {
		t.Fatalf("system parts = %s", gjson.GetBytes(out, "request.systemInstruction.parts").Raw)
	}
	if !strings.Contains(gjson.GetBytes(out, "request.contents.0.parts.0.text").String(), "You are Codex") {
		t.Fatal("contents must not be rewritten")
	}
	// snake_case variant and full removal of the instruction.
	in2 := []byte(`{"request":{"system_instruction":{"parts":[{"text":"DROPME"}]}}}`)
	out2, n2 := applyAntigravitySensitivePatterns(in2, rules)
	if n2 != 1 || gjson.GetBytes(out2, "request.system_instruction").Exists() {
		t.Fatalf("out2=%s n2=%d", out2, n2)
	}
	if out3, n3 := applyAntigravitySensitivePatterns(in, nil); n3 != 0 || string(out3) != string(in) {
		t.Fatal("empty rules must be a no-op")
	}
}

// The rewrite pipeline must run the regex pass first (so the literal
// codex-identity U+034F rule and the generic pass never see the banner), and
// record only the rule-set name in the prompt note.
func TestRewritePipelineAppliesPatternsFirstAndNotes(t *testing.T) {
	t.Setenv(genericObfuscateEnv, "off")
	cfg := &config.Config{}
	cfg.Antigravity.SensitivePatternsReplace = []config.SensitivePatternRule{prodCodexPattern}
	e := NewAntigravityExecutor(cfg)
	reporter := &helps.UsageReporter{}
	in := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"You are Codex, an agent based on GPT-5.\nWork carefully."}]}}}`)
	out, _ := e.rewriteAntigravityPromptAtLevel(context.Background(), in, reporter, 0)
	got := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String()
	if got != "You are an AI coding assistant.\nWork carefully." {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, cgj) {
		t.Fatal("U+034F must not be inserted into the rewritten banner")
	}
	if note := reporter.PromptNote(); note != "prompt_rewritten: "+sensitivePatternsRuleName {
		t.Fatalf("note=%q", note)
	}
	// Without config the legacy literal rule still applies (fallback intact).
	e2 := NewAntigravityExecutor(&config.Config{})
	out2, _ := e2.rewriteAntigravityPromptAtLevel(context.Background(), in, nil, 0)
	if !strings.Contains(gjson.GetBytes(out2, "request.systemInstruction.parts.0.text").String(), "Co"+cgj+"dex") {
		t.Fatal("legacy codex-identity obfuscation must remain the fallback")
	}
}

func TestSensitivePatternsConfigLoadDropsInvalid(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`config-version: 8
oauth:
  providers:
    antigravity:
      sensitive-patterns-replace:
        - pattern: '(?i)You are Codex,\s+(?:an?\s+)?(?:coding\s+)?agent\s+based\s+on\s+[^.\r\n"]+\.?'
          replacement: "You are an AI coding assistant."
        - pattern: '[invalid'
          replacement: "x"
        - pattern: '   '
          replacement: "y"
`))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Antigravity.SensitivePatternsReplace
	if len(got) != 1 || got[0].Replacement != "You are an AI coding assistant." {
		t.Fatalf("patterns=%#v", got)
	}
	if _, enabled := antigravityTOSPauseWindow(cfg); enabled {
		t.Fatal("tos pause must default off")
	}
}
