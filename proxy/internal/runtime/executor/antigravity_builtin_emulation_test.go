package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const emuPass1Body = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"Linux 7.3 was released on 2026-10-18."}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["latest linux kernel"],"groundingChunks":[{"web":{"title":"kernel.org","uri":"https://kernel.org/"}}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120}}}`

const emuPass2Body = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"The latest kernel is 7.3."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":300,"candidatesTokenCount":10,"totalTokenCount":310}}}`

const emuPass2SSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"The latest "}]}}],"usageMetadata":{"promptTokenCount":300,"candidatesTokenCount":3,"totalTokenCount":303}}}

data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"kernel is 7.3."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":300,"candidatesTokenCount":10,"totalTokenCount":310}}}

`

const emuUserTextRequest = `{"contents":[{"role":"user","parts":[{"text":"What is the latest Linux kernel?"}]}],"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"save_note","parameters":{"type":"object","properties":{"t":{"type":"string"}}}}]}]}`

type emuUpstream struct {
	mu        sync.Mutex
	bodies    []string
	pass1Code int
}

func (u *emuUpstream) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, string(b))
	u.mu.Unlock()
	tools := gjson.GetBytes(b, "request.tools").Raw
	if strings.Contains(tools, "googleSearch") && !strings.Contains(tools, "functionDeclarations") {
		if u.pass1Code != 0 {
			w.WriteHeader(u.pass1Code)
			_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom"}}`)
			return
		}
		_, _ = io.WriteString(w, emuPass1Body)
		return
	}
	if strings.Contains(r.URL.String(), "stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, emuPass2SSE)
		return
	}
	_, _ = io.WriteString(w, emuPass2Body)
}

func emuAuth(url string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": url},
	}
}

func withEmuMode(t *testing.T, mode string) {
	prev := antigravityHealthLive.Load()
	t.Cleanup(func() { antigravityHealthLive.Store(prev) })
	cfg := &config.AntigravityHealthConfig{}
	if mode != "" {
		cfg.BuiltinToolsWithFunctions = &mode
	}
	antigravityHealthLive.Store(cfg)
}

func runEmuExecute(t *testing.T, u *emuUpstream, payload string) cliproxyexecutor.Response {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(u.handler))
	t.Cleanup(server.Close)
	e := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	resp, err := e.Execute(context.Background(), emuAuth(server.URL), cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(payload)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return resp
}

func TestBuiltinEmulationNonStreamTwoPass(t *testing.T) {
	withEmuMode(t, "")
	u := &emuUpstream{}
	resp := runEmuExecute(t, u, emuUserTextRequest)
	if len(u.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(u.bodies))
	}
	p1, p2 := u.bodies[0], u.bodies[1]
	if strings.Contains(gjson.Get(p1, "request.tools").Raw, "functionDeclarations") || !strings.Contains(gjson.Get(p1, "request.tools").Raw, "googleSearch") {
		t.Fatalf("pass1 tools = %s", gjson.Get(p1, "request.tools").Raw)
	}
	if gjson.Get(p1, "model").String() != "gemini-2.5-flash" || gjson.Get(p2, "model").String() != "gemini-2.5-flash" {
		t.Fatalf("model changed: %s / %s", gjson.Get(p1, "model"), gjson.Get(p2, "model"))
	}
	if strings.Contains(gjson.Get(p2, "request.tools").Raw, "googleSearch") || !strings.Contains(gjson.Get(p2, "request.tools").Raw, "save_note") {
		t.Fatalf("pass2 tools = %s", gjson.Get(p2, "request.tools").Raw)
	}
	inj := gjson.Get(p2, "request.contents.@reverse.0.parts.0.text").String()
	if !strings.Contains(inj, "Linux 7.3 was released") || !strings.Contains(inj, "https://kernel.org/") || strings.Contains(inj, "thinking") {
		t.Fatalf("pass2 injected text = %q", inj)
	}
	out := resp.Payload
	if gjson.GetBytes(out, "candidates.0.groundingMetadata.groundingChunks.0.web.title").String() != "kernel.org" {
		t.Fatalf("groundingMetadata not attached: %s", out)
	}
	if got := gjson.GetBytes(out, "usageMetadata.totalTokenCount").Int(); got != 430 {
		t.Fatalf("total tokens = %d, want 430 (both passes): %s", got, out)
	}
	if got := gjson.GetBytes(out, "usageMetadata.promptTokenCount").Int(); got != 400 {
		t.Fatalf("prompt tokens = %d, want 400", got)
	}
	if h := resp.Headers.Get(antigravityBuiltinToolsHeader); h != "emulate; presearch=searched" {
		t.Fatalf("header = %q", h)
	}
}

func TestBuiltinEmulationStreamTwoPass(t *testing.T) {
	withEmuMode(t, "emulate")
	u := &emuUpstream{}
	server := httptest.NewServer(http.HandlerFunc(u.handler))
	defer server.Close()
	e := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	res, err := e.ExecuteStream(context.Background(), emuAuth(server.URL), cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(emuUserTextRequest)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var all strings.Builder
	for c := range res.Chunks {
		if c.Err != nil {
			t.Fatalf("chunk err: %v", c.Err)
		}
		all.Write(c.Payload)
		all.WriteString("\n")
	}
	if len(u.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(u.bodies))
	}
	s := all.String()
	if !strings.Contains(s, `"groundingMetadata"`) || !strings.Contains(s, "kernel.org") {
		t.Fatalf("stream lacks groundingMetadata: %s", s)
	}
	if !strings.Contains(s, `"totalTokenCount":430`) {
		t.Fatalf("stream usage not summed: %s", s)
	}
	if res.Headers.Get(antigravityBuiltinToolsHeader) == "" {
		t.Fatal("missing emulation header")
	}
}

func TestBuiltinEmulationFunctionResponseTurnSkipsSearch(t *testing.T) {
	withEmuMode(t, "")
	u := &emuUpstream{}
	req := `{"contents":[{"role":"user","parts":[{"text":"save it"}]},{"role":"model","parts":[{"functionCall":{"name":"save_note","args":{"t":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"save_note","response":{"ok":true}}}]}],"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"save_note","parameters":{"type":"object"}}]}]}`
	resp := runEmuExecute(t, u, req)
	if len(u.bodies) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(u.bodies))
	}
	if strings.Contains(gjson.Get(u.bodies[0], "request.tools").Raw, "googleSearch") {
		t.Fatalf("googleSearch must be removed from the functions pass: %s", gjson.Get(u.bodies[0], "request.tools").Raw)
	}
	if h := resp.Headers.Get(antigravityBuiltinToolsHeader); !strings.Contains(h, "function-turn") {
		t.Fatalf("header = %q", h)
	}
}

func TestBuiltinEmulationPresearchFailureFallsBack(t *testing.T) {
	withEmuMode(t, "")
	u := &emuUpstream{pass1Code: http.StatusInternalServerError}
	resp := runEmuExecute(t, u, emuUserTextRequest)
	if len(u.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(u.bodies))
	}
	if strings.Contains(u.bodies[1], "Web search results") {
		t.Fatal("no search context expected after pre-search failure")
	}
	if got := gjson.GetBytes(resp.Payload, "usageMetadata.totalTokenCount").Int(); got != 310 {
		t.Fatalf("total = %d, want 310", got)
	}
	if h := resp.Headers.Get(antigravityBuiltinToolsHeader); !strings.Contains(h, "failed") {
		t.Fatalf("header = %q", h)
	}
}

func TestBuiltinEmulationKeepAndCodeExecutionPassThrough(t *testing.T) {
	withEmuMode(t, "keep")
	u := &emuUpstream{}
	runEmuExecute(t, u, emuUserTextRequest)
	if len(u.bodies) != 1 || !strings.Contains(u.bodies[0], "googleSearch") || !strings.Contains(u.bodies[0], "save_note") {
		t.Fatalf("keep must pass through unchanged: %v", u.bodies)
	}

	withEmuMode(t, "")
	u = &emuUpstream{}
	req := strings.Replace(emuUserTextRequest, `{"googleSearch":{}}`, `{"googleSearch":{}},{"codeExecution":{}}`, 1)
	runEmuExecute(t, u, req)
	if len(u.bodies) != 1 || !strings.Contains(u.bodies[0], "codeExecution") || !strings.Contains(u.bodies[0], "googleSearch") {
		t.Fatalf("codeExecution mix must pass through: %v", u.bodies)
	}
}

func TestFoldGeminiContentsForPresearch(t *testing.T) {
	contents := gjson.Parse(`[{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"text":"q1"}]},{"role":"model","parts":[{"text":"t","thought":true,"thoughtSignature":"sig"},{"functionCall":{"name":"f","args":{"a":1}},"thoughtSignature":"sig"}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"r":"` + strings.Repeat("x", 5000) + `"}}}]},{"role":"user","parts":[{"text":"q2"}]}]`)
	out := foldGeminiContentsForPresearch(contents)
	raw := mustJSON(t, out)
	if strings.Contains(raw, "functionCall") || strings.Contains(raw, "functionResponse") || strings.Contains(raw, "thoughtSignature") || strings.Contains(raw, `"thought"`) {
		t.Fatalf("fold left function/thought parts: %s", raw)
	}
	r := gjson.Parse(raw)
	if r.Get("0.role").String() != "user" {
		t.Fatalf("first turn must be user: %s", raw)
	}
	roles := []string{}
	for _, c := range r.Array() {
		roles = append(roles, c.Get("role").String())
	}
	for i := 1; i < len(roles); i++ {
		if roles[i] == roles[i-1] {
			t.Fatalf("same-role neighbours not merged: %v", roles)
		}
	}
	if !strings.Contains(raw, "[tool call] f(") || !strings.Contains(raw, "[truncated]") || !strings.Contains(raw, "q2") {
		t.Fatalf("fold text: %s", raw)
	}
}

func TestBuiltinPresearchTimeoutCappedByAttemptLimit(t *testing.T) {
	withEmuMode(t, "")
	if got := antigravityBuiltinPresearchTimeout(22 * time.Second); got != 11*time.Second {
		t.Fatalf("got %s, want 11s", got)
	}
	if got := antigravityBuiltinPresearchTimeout(0); got != defaultBuiltinPresearchTimeout {
		t.Fatalf("got %s", got)
	}
}

func TestBuiltinEmulationDefaultMode(t *testing.T) {
	withEmuMode(t, "")
	if antigravityBuiltinToolsMode() != builtinToolsModeEmulate {
		t.Fatal("default must be emulate")
	}
	withEmuMode(t, "bogus")
	if antigravityBuiltinToolsMode() != builtinToolsModeEmulate {
		t.Fatal("unknown value must fall back to emulate")
	}
	withEmuMode(t, "drop-builtin")
	if !antigravityDropBuiltinToolsWithFunctions() {
		t.Fatal("drop-builtin")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
