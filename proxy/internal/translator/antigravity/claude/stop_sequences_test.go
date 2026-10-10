package claude

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeStopSequencesMapped(t *testing.T) {
	out, _ := ConvertClaudeRequestToAntigravity("gemini-3-flash", []byte(`{"model":"gemini-3-flash","max_tokens":10,"stop_sequences":["5"],"messages":[{"role":"user","content":"x"}]}`), false)
	if got := gjson.GetBytes(out, "request.generationConfig.stopSequences").Raw; got != `["5"]` {
		t.Fatalf("stopSequences=%s", got)
	}
}

func TestClaudeMessageIDPrefix(t *testing.T) {
	if got := claudeMessageID("abc"); got != "msg_abc" {
		t.Fatal(got)
	}
	if got := claudeMessageID("msg_x"); !strings.HasPrefix(got, "msg_") || got != "msg_x" {
		t.Fatal(got)
	}
}
