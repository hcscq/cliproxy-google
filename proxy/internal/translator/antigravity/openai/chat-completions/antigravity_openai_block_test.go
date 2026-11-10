package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityOpenAIStream_SafetyFinishIsContentFilter(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"PROHIBITED_CONTENT"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":0,"totalTokenCount":8},"modelVersion":"gemini-test","responseId":"resp-safety"}}`)
	var param any
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "gemini-test", nil, nil, raw, &param)
	if len(out) != 1 {
		t.Fatalf("chunks = %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q, body %s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.native_finish_reason").String(); got != "prohibited_content" {
		t.Fatalf("native_finish_reason = %q", got)
	}
	if !strings.Contains(gjson.GetBytes(out[0], "choices.0.delta.content").String(), "PROHIBITED_CONTENT") {
		t.Fatalf("content = %s", out[0])
	}

	done := ConvertAntigravityResponseToOpenAI(context.Background(), "gemini-test", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 0 {
		t.Fatalf("second finish chunk = %s", done[0])
	}
}

func TestAntigravityOpenAIStream_TrailingEmptyFinishKeepsContentFilter(t *testing.T) {
	var param any
	ctx := context.Background()
	first := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"SAFETY"}],"modelVersion":"gemini-test","responseId":"resp-split"}}`)
	usage := []byte(`{"response":{"candidates":[{"finishReason":""}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4},"modelVersion":"gemini-test","responseId":"resp-split"}}`)
	_ = ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, first, &param)
	out := ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, usage, &param)
	if len(out) != 1 {
		t.Fatalf("chunks = %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q, body %s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.native_finish_reason").String(); got != "safety" {
		t.Fatalf("native = %q", got)
	}
}

func TestAntigravityOpenAIStream_CandidateLessBlockThenDone(t *testing.T) {
	var param any
	ctx := context.Background()
	raw := []byte(`{"response":{"promptFeedback":{"blockReason":"OTHER","blockReasonMessage":"blocked by policy"},"modelVersion":"gemini-test","responseId":"resp-other"}}`)
	first := ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, raw, &param)
	if len(first) != 1 || !strings.Contains(gjson.GetBytes(first[0], "choices.0.delta.content").String(), "blockReason: OTHER") {
		t.Fatalf("first = %s", first)
	}
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("done chunks = %d", len(done))
	}
	if got := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q, body %s", got, done[0])
	}
	if got := gjson.GetBytes(done[0], "choices.0.native_finish_reason").String(); got != "other" {
		t.Fatalf("native = %q", got)
	}
}

func TestAntigravityOpenAINonStream_PromptBlockAndMalformed(t *testing.T) {
	ctx := context.Background()
	block := []byte(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":2,"totalTokenCount":2},"modelVersion":"gemini-test","responseId":"resp-block"}}`)
	out := ConvertAntigravityResponseToOpenAINonStream(ctx, "gemini-test", nil, nil, block, nil)
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "content_filter" {
		t.Fatalf("block finish = %s", out)
	}
	if gjson.GetBytes(out, "choices.0.native_finish_reason").String() != "prohibited_content" {
		t.Fatalf("block native = %s", out)
	}
	text := gjson.GetBytes(out, "choices.0.message.content").String()
	if !strings.Contains(text, "PROHIBITED_CONTENT") || strings.Contains(strings.ToLower(text), "account") {
		t.Fatalf("content = %q", text)
	}

	malformed := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-test","responseId":"resp-bad"}}`)
	out = ConvertAntigravityResponseToOpenAINonStream(ctx, "gemini-test", nil, nil, malformed, nil)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("malformed finish = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.native_finish_reason").String(); got != "malformed_function_call" {
		t.Fatalf("malformed native = %q", got)
	}
}

func TestAntigravityOpenAIStream_ToolCallPrecedesSafety(t *testing.T) {
	var param any
	ctx := context.Background()
	tool := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"echo","args":{"x":"1"}}}]}}]}}`)
	_ = ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, tool, &param)
	final := []byte(`{"response":{"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out := ConvertAntigravityResponseToOpenAI(ctx, "gemini-test", nil, nil, final, &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, body %s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.native_finish_reason").String(); got != "safety" {
		t.Fatalf("native = %q", got)
	}
}
