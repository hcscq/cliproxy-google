package executor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAntigravityRequestShapeOmitsPromptText(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"SECRET PROMPT"}]},{"role":"model","parts":[{"functionCall":{"name":"f"},"thoughtSignature":"s"}]}],"tools":[{}],"generationConfig":{"maxOutputTokens":256,"thinkingConfig":{"thinkingBudget":1024},"responseMimeType":"application/json"}}}`)
	shape := antigravityRequestShape(payload)
	raw, _ := json.Marshal(shape)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("shape leaks prompt text: %s", raw)
	}
	if shape["max_output_tokens"].(int64) != 256 || shape["tools"].(int) != 1 || shape["last_role"] != "model" || shape["response_mime_type"] != "application/json" {
		t.Fatalf("unexpected shape: %s", raw)
	}
	if kinds := shape["last_part_kinds"].([]string); len(kinds) != 1 || kinds[0] != "functionCall" {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestEmptySampleLimiter(t *testing.T) {
	l := &emptySampleLimiter{}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if !l.allow(now, 2) || l.allow(now.Add(time.Second), 2) {
		t.Fatal("min gap not enforced")
	}
	if !l.allow(now.Add(3*time.Second), 2) || l.allow(now.Add(10*time.Second), 2) {
		t.Fatal("hourly cap not enforced")
	}
	if !l.allow(now.Add(time.Hour), 2) {
		t.Fatal("cap must reset next hour")
	}
}

func TestLastSSEJSONExposesFinalFrame(t *testing.T) {
	raw := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n")
	out := lastSSEJSON(raw)
	if !strings.Contains(string(out), `"finishReason":"STOP"`) || !strings.Contains(string(out), "raw_sse") {
		t.Fatalf("got %s", out)
	}
}
