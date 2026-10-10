package executor

import (
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPayloadBarrierAntigravityRebuiltAttempts(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 123, "toolConfig.functionCallingConfig.mode": "CUSTOM"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: []string{"sessionId", "contents.0"}}},
	}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview"}
	body := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"user","parts":[{"text":"second"}]}]}}`)
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", body, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}))
	executor := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
	for attempt := 0; attempt < 2; attempt++ {
		request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, body, attempt == 1, "", "https://example.invalid", "injected-session")
		if errBuild != nil {
			t.Fatal(errBuild)
		}
		wire, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose := request.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if gjson.GetBytes(wire, "request.generationConfig.maxOutputTokens").Int() != 123 {
			t.Fatalf("built-in cleanup undid override: %s", wire)
		}
		if gjson.GetBytes(wire, "request.sessionId").Exists() {
			t.Fatalf("session reinjected: %s", wire)
		}
		if got := gjson.GetBytes(wire, "request.contents.#").Int(); got != 1 {
			t.Fatalf("filter applied more than once: %s", wire)
		}
	}
}

func TestPayloadBarrierAIStudio(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 900000, "generationConfig.thinkingConfig.thinkingLevel": "low"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"contents"}}},
	}}
	executor := NewAIStudioExecutor(cfg, "aistudio", nil)
	for _, stream := range []bool{false, true} {
		body, _, errTranslate := executor.translateRequest(context.Background(), cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"contents":[{"role":"model","parts":[{"text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}, stream)
		if errTranslate != nil {
			t.Fatal(errTranslate)
		}
		if gjson.GetBytes(body, "contents").Exists() || gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int() != 900000 || gjson.GetBytes(body, "generationConfig.thinkingConfig.thinkingLevel").String() != "low" {
			t.Fatalf("late normalization undid rules: %s", body)
		}
	}
}
