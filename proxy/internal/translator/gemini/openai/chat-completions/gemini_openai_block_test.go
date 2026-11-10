package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiOpenAIStream_SafetyAndPromptBlock(t *testing.T) {
	ctx := context.Background()
	var param any
	safety := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":0,"totalTokenCount":2},"modelVersion":"gemini-test","responseId":"resp-safety"}`)
	out := ConvertGeminiResponseToOpenAI(ctx, "gemini-test", nil, nil, safety, &param)
	if len(out) != 1 {
		t.Fatalf("chunks = %d", len(out))
	}
	if gjson.GetBytes(out[0], "choices.0.finish_reason").String() != "content_filter" {
		t.Fatalf("finish = %s", out[0])
	}
	if gjson.GetBytes(out[0], "choices.0.native_finish_reason").String() != "safety" {
		t.Fatalf("native = %s", out[0])
	}

	block := []byte(`{"promptFeedback":{"blockReason":"OTHER","blockReasonMessage":"blocked by policy"},"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1},"modelVersion":"gemini-test","responseId":"resp-other"}`)
	nonStream := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-test", nil, nil, block, nil)
	if gjson.GetBytes(nonStream, "choices.0.finish_reason").String() != "content_filter" || gjson.GetBytes(nonStream, "choices.0.native_finish_reason").String() != "other" {
		t.Fatalf("non-stream = %s", nonStream)
	}
	if !strings.Contains(gjson.GetBytes(nonStream, "choices.0.message.content").String(), "blockReason: OTHER") {
		t.Fatalf("content = %s", nonStream)
	}
}
