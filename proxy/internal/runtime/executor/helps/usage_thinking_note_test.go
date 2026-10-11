package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageReporterThinkingNoteUsesResolvedModel(t *testing.T) {
	ctx := usage.WithReasoningEffort(context.Background(), "none")
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}}`)
	for mode, wantNote := range map[string]bool{"zero-reduced": true, "zero": false} {
		r := NewUsageReporter(ctx, "antigravity", "model-not-in-registry", nil)
		r.SetModelThinking(&registry.ModelInfo{ID: "m", Thinking: &registry.ThinkingSupport{OffMode: mode}})
		r.SetTranslatedReasoningEffort(body, "antigravity")
		if r.thinkingParams != "budget=0" {
			t.Fatalf("%s: params %q", mode, r.thinkingParams)
		}
		if (r.ThinkingNote() != "") != wantNote {
			t.Fatalf("%s: note %q", mode, r.ThinkingNote())
		}
	}
}
