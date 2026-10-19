package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeMixedWebSearchRequest = `{"model":"gemini-2.5-flash","max_tokens":1000,"messages":[{"role":"user","content":"What is the latest Linux kernel?"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5},{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`

func runClaudeExec(t *testing.T, u *emuUpstream, payload string, stream bool) (string, []string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(u.handler))
	t.Cleanup(server.Close)
	e := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	req := cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: stream}
	if !stream {
		resp, err := e.Execute(context.Background(), emuAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var types []string
		for _, b := range gjson.GetBytes(resp.Payload, "content").Array() {
			types = append(types, b.Get("type").String())
		}
		return string(resp.Payload), types
	}
	res, err := e.ExecuteStream(context.Background(), emuAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var all strings.Builder
	var types []string
	for c := range res.Chunks {
		if c.Err != nil {
			t.Fatalf("chunk: %v", c.Err)
		}
		all.Write(c.Payload)
		for _, line := range strings.Split(string(c.Payload), "\n") {
			if strings.HasPrefix(line, "data:") {
				ev := gjson.Parse(strings.TrimSpace(line[5:]))
				if ev.Get("type").String() == "content_block_start" {
					types = append(types, ev.Get("content_block.type").String())
				}
			}
		}
	}
	return all.String(), types
}

func TestClaudeWebSearchWithFunctionsByMode(t *testing.T) {
	for _, stream := range []bool{false, true} {
		withEmuMode(t, "") // default emulate
		u := &emuUpstream{}
		out, types := runClaudeExec(t, u, claudeMixedWebSearchRequest, stream)
		if len(u.bodies) != 2 {
			t.Fatalf("stream=%v emulate: upstream calls = %d, want 2", stream, len(u.bodies))
		}
		p1, p2 := gjson.Get(u.bodies[0], "request.tools").Raw, gjson.Get(u.bodies[1], "request.tools").Raw
		if !strings.Contains(p1, "googleSearch") || strings.Contains(p1, "Bash") || strings.Contains(p2, "googleSearch") || !strings.Contains(p2, "Bash") {
			t.Fatalf("stream=%v pass tools: %s | %s", stream, p1, p2)
		}
		if len(types) < 3 || types[0] != "server_tool_use" || types[1] != "web_search_tool_result" || types[2] != "text" {
			t.Fatalf("stream=%v block types = %v\n%s", stream, types, out)
		}
		if !strings.Contains(out, "https://kernel.org/") || !strings.Contains(out, `"web_search_requests":1`) {
			t.Fatalf("stream=%v results/usage missing: %s", stream, out)
		}
	}

	withEmuMode(t, "keep")
	u := &emuUpstream{}
	runClaudeExec(t, u, claudeMixedWebSearchRequest, false)
	if len(u.bodies) != 1 || !strings.Contains(gjson.Get(u.bodies[0], "request.tools").Raw, "googleSearch") || !strings.Contains(u.bodies[0], "Bash") {
		t.Fatalf("keep must send googleSearch + functions (upstream 400s): %v", u.bodies)
	}

	withEmuMode(t, "drop-builtin")
	u = &emuUpstream{}
	runClaudeExec(t, u, claudeMixedWebSearchRequest, false)
	if len(u.bodies) != 1 || strings.Contains(u.bodies[0], "googleSearch") || !strings.Contains(u.bodies[0], "Bash") {
		t.Fatalf("drop-builtin must keep the old silent drop: %v", u.bodies)
	}
}

func TestClaudeWebSearchOnlyPathUnchanged(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("test-claude-ws-only", "antigravity", []*registry.ModelInfo{{ID: "gemini-2.5-flash", SupportsWebSearch: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("test-claude-ws-only") })
	withEmuMode(t, "")
	u := &emuUpstream{}
	req := `{"model":"gemini-2.5-flash","max_tokens":1000,"messages":[{"role":"user","content":"Perform a web search for the query: kernel"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}]}`
	_, types := runClaudeExec(t, u, req, false)
	if len(u.bodies) != 1 || gjson.Get(u.bodies[0], "requestType").String() != "web_search" || strings.Contains(u.bodies[0], "functionDeclarations") {
		t.Fatalf("search-only must use the native web_search request once: %v", u.bodies)
	}
	if len(types) < 2 || types[0] != "server_tool_use" || types[1] != "web_search_tool_result" {
		t.Fatalf("types = %v", types)
	}
}
