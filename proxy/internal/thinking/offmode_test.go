package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
	"github.com/tidwall/gjson"
)

func gemini3Model(id, offMode string, levels ...string) *registry.ModelInfo {
	if len(levels) == 0 {
		levels = []string{"low", "medium", "high"}
	}
	return &registry.ModelInfo{ID: id, Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, DynamicAllowed: true, Levels: levels, OffMode: offMode}}
}

// "Thinking off" per measured model capability, end to end through validation
// and the Antigravity applier.
func TestThinkingOffFollowsPerModelOffMode(t *testing.T) {
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"includeThoughts":true}}}}`)
	cases := []struct {
		name, offMode string
		levels        []string
		want          string
	}{
		{"truly off", thinking.OffModeZero, nil, "budget=0"},
		{"reduced", thinking.OffModeZeroReduced, nil, "budget=0"},
		{"omit", thinking.OffModeOmit, nil, ""},
		{"minimal", thinking.OffModeMinimal, []string{"minimal", "low", "high"}, "level=minimal,include_thoughts=true"},
		{"unknown model keeps lowest level", "", nil, "level=low,include_thoughts=true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := gemini3Model("gemini-test", c.offMode, c.levels...)
			cfg, err := thinking.ValidateConfig(thinking.ThinkingConfig{Mode: thinking.ModeNone}, info, "gemini", "antigravity", false)
			if err != nil {
				t.Fatal(err)
			}
			out, err := antigravity.NewApplier().Apply(body, *cfg, info)
			if err != nil {
				t.Fatal(err)
			}
			got := thinking.ExactThinkingParams(out, "antigravity")
			if got != c.want {
				t.Fatalf("sent %q, want %q (body %s)", got, c.want, out)
			}
			if c.offMode == thinking.OffModeOmit && gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() {
				t.Fatalf("omit must drop thinkingConfig: %s", out)
			}
		})
	}
}

// Regression: the OpenAI translator emits thinkingLevel="none"; the explicit zero
// budget must replace it (the upstream rejects level "none" with 400).
func TestThinkingOffFromTranslatedLevelNone(t *testing.T) {
	src := []byte(`{"model":"x","reasoning_effort":"none","messages":[]}`)
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"none"}}}}`)
	for mode, want := range map[string]string{
		thinking.OffModeZeroReduced: "budget=0",
		thinking.OffModeZero:        "budget=0",
		thinking.OffModeOmit:        "",
		"":                          "level=low,include_thoughts=false",
	} {
		info := gemini3Model("gemini-3.8-flash-tiered", mode)
		out, err := thinking.ApplyThinkingWithModelInfo(body, src, info.ID, "openai", "antigravity", "antigravity", info)
		if err != nil {
			t.Fatal(err)
		}
		if got := thinking.ExactThinkingParams(out, "antigravity"); got != want {
			t.Errorf("off mode %q: sent %q want %q (%s)", mode, got, want, out)
		}
		if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String() == "none" {
			t.Errorf("off mode %q: thinkingLevel none leaked upstream: %s", mode, out)
		}
	}
	if n := thinking.OffAdjustmentNote("none", "level=minimal", nil); n == "" {
		t.Error("unknown model capabilities must still report a non-off value")
	}
}

func TestExactThinkingParams(t *testing.T) {
	cases := map[string]string{
		`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192,"includeThoughts":true}}}}`: "budget=8192,include_thoughts=true",
		`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}}`:                      "level=high",
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`:                                     "budget=-1",
		`{"thinking":{"type":"enabled","budget_tokens":4096}}`:                                              "budget=4096",
		`{"thinking":{"type":"disabled"}}`:                                                                  "disabled",
		`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`:                                 "adaptive,effort=max",
		`{"reasoning_effort":"medium"}`:                                                                     "effort=medium",
		`{"messages":[]}`:                                                                                   "",
	}
	for body, want := range cases {
		if got := thinking.ExactThinkingParams([]byte(body), "antigravity"); got != want {
			t.Errorf("%s: got %q want %q", body, got, want)
		}
	}
}

func TestOffAdjustmentNote(t *testing.T) {
	s := func(m string) *registry.ThinkingSupport { return &registry.ThinkingSupport{OffMode: m} }
	if n := thinking.OffAdjustmentNote("low", "level=low", s("")); n != "" {
		t.Fatalf("not an off request: %q", n)
	}
	for _, m := range []string{thinking.OffModeZero, thinking.OffModeOmit} {
		if n := thinking.OffAdjustmentNote("none", "budget=0", s(m)); n != "" {
			t.Fatalf("%s is really off: %q", m, n)
		}
	}
	if n := thinking.OffAdjustmentNote("none", "budget=0", s(thinking.OffModeZeroReduced)); n == "" {
		t.Fatal("reduced must be reported")
	}
	if n := thinking.OffAdjustmentNote("none", "level=low", s("")); n != "thinking cannot be disabled on this model; sent level=low instead" {
		t.Fatalf("fallback note: %q", n)
	}
	if n := thinking.OffAdjustmentNote("none", "level=minimal", s(thinking.OffModeMinimal)); n == "" {
		t.Fatal("minimal must be reported")
	}
}
