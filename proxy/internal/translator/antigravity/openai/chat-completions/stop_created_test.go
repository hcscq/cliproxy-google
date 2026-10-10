package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenAIStopMapsToStopSequences(t *testing.T) {
	out, _ := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(`{"messages":[{"role":"user","content":"x"}],"stop":["5","END"]}`), false)
	if got := gjson.GetBytes(out, "request.generationConfig.stopSequences").Raw; got != `["5","END"]` {
		t.Fatalf("stopSequences=%s", got)
	}
	out, _ = ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(`{"messages":[{"role":"user","content":"x"}],"stop":"5"}`), false)
	if got := gjson.GetBytes(out, "request.generationConfig.stopSequences").Raw; got != `["5"]` {
		t.Fatalf("string stop -> %s", got)
	}
}

func TestOpenAINonStreamCreatedIsSet(t *testing.T) {
	var param any
	out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "gemini-3-flash", nil, nil, []byte(`{"response":{"responseId":"r1","candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}}`), &param)
	if gjson.GetBytes(out, "created").Int() < 1700000000 {
		t.Fatalf("created not set: %s", out)
	}
}
