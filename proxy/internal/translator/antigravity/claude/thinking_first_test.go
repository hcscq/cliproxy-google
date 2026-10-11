package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeThinkingFirst(t *testing.T) {
	in := []byte(`{"content":[{"type":"text","text":"a"},{"type":"thinking","thinking":"t"}]}`)
	out := claudeThinkingFirst(in)
	if gjson.GetBytes(out, "content.0.type").String() != "thinking" || gjson.GetBytes(out, "content.1.text").String() != "a" {
		t.Fatalf("%s", out)
	}
	ok := []byte(`{"content":[{"type":"thinking","thinking":"t"},{"type":"text","text":"a"}]}`)
	if string(claudeThinkingFirst(ok)) != string(ok) {
		t.Fatal("already ordered content must be unchanged")
	}
}
