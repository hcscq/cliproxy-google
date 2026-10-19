package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/tidwall/gjson"
)

func TestRepairGeminiFunctionResponseNamesPlaceholder(t *testing.T) {
	in := []byte(`{"request":{"contents":[
{"role":"user","parts":[{"text":"go"}]},
{"role":"model","parts":[{"functionCall":{"name":"image_edit","args":{}}},{"functionCall":{"name":"image_crop","args":{}}}]},
{"role":"user","parts":[{"functionResponse":{"name":"unknown_function","response":{"r":1}}},{"functionResponse":{"name":"","response":{"r":2}}}]}]}}`)
	if internalsignature.ValidateGeminiFunctionCallPairing(in) == nil {
		t.Fatal("fixture should fail pairing before repair")
	}
	out := repairGeminiFunctionResponseNames(in, "request.contents")
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "image_edit" {
		t.Fatalf("part0 name = %q", got)
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.name").String(); got != "image_crop" {
		t.Fatalf("part1 name = %q", got)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("pairing after repair: %v", err)
	}
}

func TestRepairGeminiFunctionResponseNamesByIDAndKeepsValid(t *testing.T) {
	in := []byte(`{"request":{"contents":[
{"role":"model","parts":[{"functionCall":{"id":"a","name":"read"}},{"functionCall":{"id":"b","name":"write"}}]},
{"role":"user","parts":[{"functionResponse":{"id":"b","name":"unknown_function","response":{}}},{"functionResponse":{"id":"a","name":"read","response":{}}}]}]}}`)
	out := repairGeminiFunctionResponseNames(in, "request.contents")
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse.name").String(); got != "write" {
		t.Fatalf("by-id repair = %q, want write", got)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.functionResponse.name").String(); got != "read" {
		t.Fatalf("valid name changed to %q", got)
	}
	ok := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"x"}}]},{"role":"user","parts":[{"functionResponse":{"name":"x","response":{}}}]}]}}`)
	if string(repairGeminiFunctionResponseNames(ok, "request.contents")) != string(ok) {
		t.Fatal("valid payload must be unchanged")
	}
}

func TestDropGeminiBuiltinToolsWhenFunctions(t *testing.T) {
	mixed := []byte(`{"request":{"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"save_note"}]}]}}`)
	out := dropGeminiBuiltinToolsWhenFunctions(mixed, "request.tools")
	tools := gjson.GetBytes(out, "request.tools").Array()
	if len(tools) != 1 || tools[0].Get("functionDeclarations.0.name").String() != "save_note" {
		t.Fatalf("tools = %s", gjson.GetBytes(out, "request.tools").Raw)
	}
	only := []byte(`{"request":{"tools":[{"googleSearch":{}}]}}`)
	if string(dropGeminiBuiltinToolsWhenFunctions(only, "request.tools")) != string(only) {
		t.Fatal("built-in-only request must be unchanged")
	}
}

func TestAntigravity400CompatSettingsOff(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	no, keep := false, "keep"
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RepairFunctionResponseNames: &no, BuiltinToolsWithFunctions: &keep})
	if antigravityRepairFunctionResponseNamesEnabled() || antigravityDropBuiltinToolsWithFunctions() {
		t.Fatal("settings off must disable the repairs")
	}
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{})
	if !antigravityRepairFunctionResponseNamesEnabled() || !antigravityDropBuiltinToolsWithFunctions() {
		t.Fatal("defaults must be on")
	}
}

func TestEnsureBoundaryAppliesFunctionNameRepair(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{})
	in := []byte(`{"model":"gemini-3.8-flash-tiered","request":{"contents":[{"role":"user","parts":[{"text":"q"}]},{"role":"model","parts":[{"functionCall":{"name":"list_artifacts","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"unknown_function","response":{}}}]}],"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"list_artifacts"}]}]}}`)
	out := ensureAntigravityGeminiBoundaryUserContent("gemini-3.8-flash-tiered", in)
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "list_artifacts" {
		t.Fatalf("name = %q", got)
	}
	if gjson.GetBytes(out, "request.tools.#").Int() != 1 {
		t.Fatalf("tools = %s", gjson.GetBytes(out, "request.tools").Raw)
	}
	if gjson.GetBytes(out, "model").String() != "gemini-3.8-flash-tiered" {
		t.Fatal("model must never change")
	}
}
