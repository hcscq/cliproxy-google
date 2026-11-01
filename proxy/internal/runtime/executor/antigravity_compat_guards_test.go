package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/tidwall/gjson"
)

func TestRepairGeminiFunctionResponseNamesByIDEvenWhenNameMatchesOtherCall(t *testing.T) {
	in := []byte(`{"request":{"contents":[
{"role":"model","parts":[{"functionCall":{"id":"1","name":"list_files"}},{"functionCall":{"id":"2","name":"package_proxy"}}]},
{"role":"user","parts":[{"functionResponse":{"id":"2","name":"list_files","response":{}}},{"functionResponse":{"id":"1","name":"package_proxy","response":{}}}]}]}}`)
	out := repairGeminiFunctionResponseNames(in, "request.contents")
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse.name").String(); got != "package_proxy" {
		t.Fatalf("resp0 name = %q, want package_proxy", got)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.functionResponse.name").String(); got != "list_files" {
		t.Fatalf("resp1 name = %q, want list_files", got)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("pairing after id repair: %v", err)
	}
}

func TestRepairGeminiFunctionResponseNamesProdListFilesVsPackageProxy(t *testing.T) {
	in := []byte(`{"request":{"contents":[
{"role":"user","parts":[{"text":"x"}]},
{"role":"model","parts":[{"functionCall":{"name":"package_proxy","args":{}}}]},
{"role":"user","parts":[{"functionResponse":{"name":"list_files","response":{"ok":true}}}]}]}}`)
	out := repairGeminiFunctionResponseNames(in, "request.contents")
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "package_proxy" {
		t.Fatalf("name = %q", got)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("pairing: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairingByID(t *testing.T) {
	in := []byte(`{"request":{"contents":[
{"role":"model","parts":[{"functionCall":{"id":"a","name":"read"}},{"functionCall":{"id":"b","name":"write"}}]},
{"role":"user","parts":[{"functionResponse":{"id":"b","name":"write","response":{}}},{"functionResponse":{"id":"a","name":"read","response":{}}}]}]}}`)
	if err := internalsignature.ValidateGeminiFunctionCallPairing(in); err != nil {
		t.Fatalf("id-ordered responses should pass: %v", err)
	}
}

func TestContextPrecheckRejectsOverLimit(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{})
	info := &registry.ModelInfo{InputTokenLimit: 10}
	payload := []byte(strings.Repeat("x", 200)) // ~50 tokens
	err := antigravityContextPrecheckError("gemini-3.8-flash", info, payload)
	if err == nil {
		t.Fatal("expected precheck error")
	}
	se, ok := err.(statusErr)
	if !ok || se.StatusCode() != http.StatusBadRequest || !strings.Contains(err.Error(), "context length exceeded") {
		t.Fatalf("bad error: %v", err)
	}
	off := false
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{ContextTokenPrecheck: &off})
	if err := antigravityContextPrecheckError("gemini-3.8-flash", info, payload); err != nil {
		t.Fatalf("disabled precheck should pass: %v", err)
	}
}

func TestStripAntigravitySignedThinkingForRetry(t *testing.T) {
	in := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"abc123"},{"type":"text","text":"hi"}]}],"request":{"contents":[{"role":"model","parts":[{"thought":true,"text":"t","thoughtSignature":"sig"},{"text":"ok"}]}]}}`)
	out, n := stripAntigravitySignedThinkingForRetry(in)
	if n < 2 {
		t.Fatalf("removed=%d out=%s", n, out)
	}
	if gjson.GetBytes(out, "messages.0.content.#").Int() != 1 {
		t.Fatalf("thinking block not stripped: %s", out)
	}
	if gjson.GetBytes(out, "request.contents.0.parts.#").Int() != 1 {
		t.Fatalf("thought part not stripped: %s", out)
	}
}

func TestAntigravityInvalidThinkingSignatureBody(t *testing.T) {
	body := []byte(`{"error":{"message":"messages.1.content.0: Invalid signature in thinking block"}}`)
	if !antigravityInvalidThinkingSignatureBody(body) {
		t.Fatal("expected detect")
	}
	if antigravityInvalidThinkingSignatureBody([]byte(`{"error":"other"}`)) {
		t.Fatal("false positive")
	}
}
