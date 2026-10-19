package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestClampThinkingLevelSettingGeminiSameFamily(t *testing.T) {
	defer SetClampUnsupportedLevels(true)
	mi := &registry.ModelInfo{ID: "gemini-3.6-flash", Type: "gemini", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}
	cfg := ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel("minimal")}
	SetClampUnsupportedLevels(true)
	got, err := ValidateConfig(cfg, mi, "gemini", "gemini", false)
	if err != nil || got.Level != ThinkingLevel("low") {
		t.Fatalf("clamp on: got %+v err %v, want low", got, err)
	}
	SetClampUnsupportedLevels(false)
	if _, err := ValidateConfig(cfg, mi, "gemini", "gemini", false); err == nil {
		t.Fatal("clamp off: want error for unsupported level")
	}
	SetClampUnsupportedLevels(true)
	oi := &registry.ModelInfo{ID: "o", Type: "openai", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}
	if _, err := ValidateConfig(ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel("xhigh")}, oi, "openai", "openai", false); err == nil {
		t.Fatal("non-Gemini targets must stay strict")
	}
}
