package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func modePtr(m config.CompatMode) *config.CompatMode { return &m }

func TestRewriteToolRolePairedAndUnpaired(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeOn)})

	// Paired: model functionCall then role=tool text → user functionResponse
	pairedIn := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"list"}]},
		{"role":"model","parts":[{"functionCall":{"id":"c1","name":"ls","args":{}}}]},
		{"role":"tool","parts":[{"text":"a.txt"}]},
		{"role":"user","parts":[{"text":"ok"}]}
	]}}`)
	out := rewriteAntigravityToolRoles(pairedIn, "request.contents")
	c := gjson.GetBytes(out, "request.contents").Array()
	if len(c) != 4 {
		t.Fatalf("len=%d %s", len(c), out)
	}
	if c[2].Get("role").String() != "user" {
		t.Fatalf("role=%s", c[2].Get("role").String())
	}
	if c[2].Get("parts.0.functionResponse.name").String() != "ls" {
		t.Fatalf("fr name=%s raw=%s", c[2].Get("parts.0.functionResponse.name").String(), c[2].Raw)
	}
	if c[2].Get("parts.0.functionResponse.id").String() != "c1" {
		t.Fatalf("fr id=%s", c[2].Get("parts.0.functionResponse.id").String())
	}
	if c[2].Get("parts.0.functionResponse.response.result").String() != "a.txt" {
		t.Fatalf("result=%q", c[2].Get("parts.0.functionResponse.response.result").String())
	}

	// Unpaired: tool without prior functionCall → user keeping text
	unpairedIn := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"list"}]},
		{"role":"model","parts":[{"text":"calling"}]},
		{"role":"tool","parts":[{"text":"a.txt"}]},
		{"role":"user","parts":[{"text":"ok"}]}
	]}}`)
	out = rewriteAntigravityToolRoles(unpairedIn, "request.contents")
	c = gjson.GetBytes(out, "request.contents").Array()
	if c[2].Get("role").String() != "user" || c[2].Get("parts.0.text").String() != "a.txt" {
		t.Fatalf("unpaired=%s", c[2].Raw)
	}
	if c[2].Get("parts.0.functionResponse").Exists() {
		t.Fatalf("unpaired should keep text, not invent FR: %s", c[2].Raw)
	}
}

func TestRewriteToolRoleParallelPositionPairing(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeOn)})

	in := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"list"}]},
		{"role":"model","parts":[
			{"functionCall":{"id":"c1","name":"ls","args":{}}},
			{"functionCall":{"id":"c2","name":"pwd","args":{}}}
		]},
		{"role":"tool","parts":[{"text":"a"}]},
		{"role":"tool","parts":[{"text":"/tmp"}]}
	]}}`)
	out := rewriteAntigravityToolRoles(in, "request.contents")
	c := gjson.GetBytes(out, "request.contents").Array()
	if c[2].Get("parts.0.functionResponse.name").String() != "ls" || c[2].Get("parts.0.functionResponse.response.result").String() != "a" {
		t.Fatalf("first=%s", c[2].Raw)
	}
	if c[3].Get("parts.0.functionResponse.name").String() != "pwd" || c[3].Get("parts.0.functionResponse.response.result").String() != "/tmp" {
		t.Fatalf("second=%s", c[3].Raw)
	}
}

func TestRewriteToolRoleModesAndNormalRequest(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)

	normal := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"yo"}]}]}}`)
	toolish := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"x"}]},{"role":"tool","parts":[{"text":"t"}]}]}}`)

	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeOff)})
	if string(rewriteAntigravityToolRoles(toolish, "request.contents")) != string(toolish) {
		t.Fatal("off should not rewrite")
	}

	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeObserve)})
	if string(rewriteAntigravityToolRoles(toolish, "request.contents")) != string(toolish) {
		t.Fatal("observe should not rewrite")
	}

	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeOn)})
	if string(rewriteAntigravityToolRoles(normal, "request.contents")) != string(normal) {
		t.Fatal("normal request changed")
	}

	// Claude model path applies rewrite via ensure
	out := ensureAntigravityGeminiBoundaryUserContent("claude-sonnet-5-5-high", toolish)
	if gjson.GetBytes(out, "request.contents.2.role").String() != "user" {
		t.Fatalf("claude ensure did not rewrite: %s", out)
	}
	// Gemini path also
	out = ensureAntigravityGeminiBoundaryUserContent("gemini-2.5-flash", toolish)
	if gjson.GetBytes(out, "request.contents.2.role").String() != "user" {
		t.Fatalf("gemini ensure did not rewrite: %s", out)
	}
}

func TestRewriteToolRoleByToolCallID(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RewriteToolRoleMode: modePtr(config.CompatModeOn)})

	// Out-of-order ids: second tool references c1 first
	in := []byte(`{"request":{"contents":[
		{"role":"model","parts":[
			{"functionCall":{"id":"c1","name":"ls","args":{}}},
			{"functionCall":{"id":"c2","name":"pwd","args":{}}}
		]},
		{"role":"tool","tool_call_id":"c2","parts":[{"text":"/tmp"}]},
		{"role":"tool","tool_call_id":"c1","parts":[{"text":"a"}]}
	]}}`)
	out := rewriteAntigravityToolRoles(in, "request.contents")
	c := gjson.GetBytes(out, "request.contents").Array()
	if c[1].Get("parts.0.functionResponse.name").String() != "pwd" {
		t.Fatalf("want pwd got %s", c[1].Raw)
	}
	if c[2].Get("parts.0.functionResponse.name").String() != "ls" {
		t.Fatalf("want ls got %s", c[2].Raw)
	}
}
