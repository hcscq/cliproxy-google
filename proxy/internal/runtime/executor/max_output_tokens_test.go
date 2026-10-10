package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestFitGeminiMaxOutputTokens(t *testing.T) {
	off := false
	cases := []struct {
		name       string
		cfg        *config.Config
		in         string
		wantMax    int64 // -1 = absent
		wantBudget int64 // -99 = absent
	}{
		{"no limit stays absent", nil, `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048}}}}`, -1, 2048},
		{"limit kept, budget below", nil, `{"request":{"generationConfig":{"maxOutputTokens":4096,"thinkingConfig":{"thinkingBudget":1024}}}}`, 4096, 1024},
		{"budget >= limit lowered", nil, `{"request":{"generationConfig":{"maxOutputTokens":1000,"thinkingConfig":{"thinkingBudget":2048}}}}`, 1000, 500},
		{"dynamic budget untouched", nil, `{"request":{"generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1}}}}`, 100, -1},
		{"level untouched", nil, `{"request":{"generationConfig":{"maxOutputTokens":5,"thinkingConfig":{"thinkingLevel":"high"}}}}`, 5, -99},
		{"toggle off strips", &config.Config{HonorMaxTokens: &off}, `{"request":{"generationConfig":{"maxOutputTokens":5}}}`, -1, -99},
	}
	for _, tc := range cases {
		out := fitGeminiMaxOutputTokens(tc.cfg, []byte(tc.in), "request.", "unknown-test-model")
		m := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens")
		if (tc.wantMax == -1 && m.Exists()) || (tc.wantMax != -1 && m.Int() != tc.wantMax) {
			t.Errorf("%s: maxOutputTokens=%s", tc.name, m.Raw)
		}
		b := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget")
		if (tc.wantBudget == -99 && b.Exists()) || (tc.wantBudget != -99 && b.Int() != tc.wantBudget) {
			t.Errorf("%s: thinkingBudget=%s", tc.name, b.Raw)
		}
	}
	// AI Studio layout (no "request." prefix)
	out := fitGeminiMaxOutputTokens(nil, []byte(`{"generationConfig":{"maxOutputTokens":64}}`), "", "x")
	if gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int() != 64 {
		t.Fatalf("aistudio: %s", out)
	}
}

func TestAntigravityBuildRequestHonorMaxTokensToggle(t *testing.T) {
	off := false
	executor := &AntigravityExecutor{cfg: &config.Config{HonorMaxTokens: &off}}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "p"}}
	req, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3-flash", []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":7}}}`), false, "", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, req)
	if gc, ok := body["request"].(map[string]any)["generationConfig"].(map[string]any); ok {
		if _, has := gc["maxOutputTokens"]; has {
			t.Fatal("toggle off must strip maxOutputTokens")
		}
	}
}
