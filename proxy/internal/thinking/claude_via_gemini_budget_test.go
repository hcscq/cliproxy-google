package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Claude models behind Antigravity are typed as a Gemini-family provider; a
// Gemini-format request with a budget below Claude's minimum must be clamped
// (as Google clamps budgets), not rejected with 400.
func TestGeminiBudgetClampedForClaudeModelViaAntigravity(t *testing.T) {
	info := &registry.ModelInfo{ID: "claude-sonnet-4-6", Type: "antigravity", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000}}
	got, err := ValidateConfig(ThinkingConfig{Mode: ModeBudget, Budget: 512}, info, "gemini", "antigravity", false)
	if err != nil {
		t.Fatalf("expected clamp, got error %v", err)
	}
	if got.Budget != 1024 {
		t.Fatalf("budget = %d, want 1024", got.Budget)
	}
	// Gemini models keep strict validation for same-family requests.
	gem := &registry.ModelInfo{ID: "gemini-2.5-flash", Type: "antigravity", Thinking: &registry.ThinkingSupport{Min: 1, Max: 24576}}
	if _, err := ValidateConfig(ThinkingConfig{Mode: ModeBudget, Budget: 99999}, gem, "gemini", "antigravity", false); err == nil {
		t.Fatalf("gemini model: expected strict out-of-range error")
	}
}
