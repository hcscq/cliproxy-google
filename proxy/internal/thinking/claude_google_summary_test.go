package thinking

import "testing"

func TestClaudeDefaultSummaryForGoogleTarget(t *testing.T) {
	src := []byte(`{"thinking":{"type":"enabled","budget_tokens":2000},"messages":[]}`)
	if got := claudeDefaultSummaryForGoogleTarget(SummaryConfig{}, src, "claude", "antigravity"); got.Mode != SummaryEnabled {
		t.Fatalf("antigravity target: %+v", got)
	}
	if got := claudeDefaultSummaryForGoogleTarget(SummaryConfig{}, src, "claude", "claude"); got.Mode != SummaryUnspecified {
		t.Fatalf("claude target must keep model default: %+v", got)
	}
	if got := claudeDefaultSummaryForGoogleTarget(SummaryConfig{Mode: SummaryDisabled}, src, "claude", "gemini"); got.Mode != SummaryDisabled {
		t.Fatalf("explicit display must win: %+v", got)
	}
	if got := claudeDefaultSummaryForGoogleTarget(SummaryConfig{}, []byte(`{"messages":[]}`), "claude", "gemini"); got.Mode != SummaryUnspecified {
		t.Fatalf("no thinking: %+v", got)
	}
}
