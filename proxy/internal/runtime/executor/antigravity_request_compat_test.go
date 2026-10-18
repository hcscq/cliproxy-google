package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestBoundaryDropsDatalessParts(t *testing.T) {
	in := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"},{},{"thought":true},{"thoughtSignature":"x"},{"text":""}]},{"role":"model","parts":[{"thought":true}]},{"role":"user","parts":[{"text":"q"}]}]}}`)
	out := ensureAntigravityGeminiBoundaryUserContent("gemini-2.5-flash", in)
	c := gjson.GetBytes(out, "request.contents")
	if n := len(c.Array()); n != 2 {
		t.Fatalf("contents=%d want 2: %s", n, c.Raw)
	}
	if got := c.Get("0.parts").Raw; got != `[{"text":"hi"},{"text":""}]` {
		t.Fatalf("parts=%s", got)
	}
}

func TestBoundaryTrailingModelTurnGetsNonEmptyUserTurn(t *testing.T) {
	in := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"Hel"}]}]}}`)
	out := ensureAntigravityGeminiBoundaryUserContent("gemini-3.8-flash-tiered", in)
	c := gjson.GetBytes(out, "request.contents").Array()
	if len(c) != 3 || c[2].Get("role").String() != "user" || c[2].Get("parts.0.text").String() != "Continue." || len(c[2].Get("parts").Array()) != 1 {
		t.Fatalf("got %s", gjson.GetBytes(out, "request.contents").Raw)
	}
	// Function-response tail is untouched.
	fr := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"name":"f"}}]},{"role":"model","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}}`)
	if out := ensureAntigravityGeminiBoundaryUserContent("gemini-2.5-flash", fr); len(gjson.GetBytes(out, "request.contents").Array()) != 3 {
		t.Fatalf("function response tail changed: %s", out)
	}
}

func TestBoundaryCompatSettings(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	off, no := "off", false
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{TrailingModelTurnText: &off, DropEmptyParts: &no})
	in := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"},{}]},{"role":"model","parts":[{"text":"Hel"}]}]}}`)
	out := ensureAntigravityGeminiBoundaryUserContent("gemini-2.5-flash", in)
	c := gjson.GetBytes(out, "request.contents").Array()
	if len(c[0].Get("parts").Array()) != 2 || c[len(c)-1].Get("parts.0.text").String() != "" {
		t.Fatalf("settings ignored: %s", out)
	}
	custom := "Please continue."
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{TrailingModelTurnText: &custom})
	out = ensureAntigravityGeminiBoundaryUserContent("gemini-2.5-flash", in)
	if gjson.GetBytes(out, "request.contents.2.parts.0.text").String() != custom {
		t.Fatalf("custom text: %s", out)
	}
	// Claude targets untouched.
	if string(ensureAntigravityGeminiBoundaryUserContent("claude-sonnet-4", in)) != string(in) {
		t.Fatal("claude changed")
	}
}
