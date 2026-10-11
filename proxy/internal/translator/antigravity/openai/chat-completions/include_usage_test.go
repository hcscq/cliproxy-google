package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

const finalChunk = `{"response":{"responseId":"r1","modelVersion":"gemini-3-flash","candidates":[{"content":{"role":"model","parts":[{"text":"391"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":3,"cachedContentTokenCount":80,"totalTokenCount":103}}}`

func TestIncludeUsageEmitsTrailingUsageOnlyChunk(t *testing.T) {
	var param any
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "", []byte(`{"stream":true,"stream_options":{"include_usage":true}}`), nil, []byte(finalChunk), &param)
	if len(out) != 2 {
		t.Fatalf("chunks = %d: %q", len(out), out)
	}
	if gjson.GetBytes(out[0], "usage").Exists() {
		t.Fatalf("content chunk must not carry usage: %s", out[0])
	}
	if gjson.GetBytes(out[0], "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("finish_reason lost: %s", out[0])
	}
	tail := gjson.ParseBytes(out[1])
	if !tail.Get("choices").IsArray() || len(tail.Get("choices").Array()) != 0 || tail.Get("id").String() != "r1" {
		t.Fatalf("usage chunk shape: %s", out[1])
	}
	if tail.Get("usage.prompt_tokens").Int() != 100 || tail.Get("usage.prompt_tokens_details.cached_tokens").Int() != 80 ||
		!tail.Get("usage.completion_tokens_details.reasoning_tokens").Exists() {
		t.Fatalf("usage = %s", tail.Get("usage").Raw)
	}
}

func TestUsageDetailsAlwaysPresentWithoutIncludeUsage(t *testing.T) {
	var param any
	plain := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"x"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}}`
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "", []byte(`{"stream":true}`), nil, []byte(plain), &param)
	if len(out) != 1 {
		t.Fatalf("chunks = %d", len(out))
	}
	u := gjson.GetBytes(out[0], "usage")
	if !u.Get("prompt_tokens_details.cached_tokens").Exists() || !u.Get("completion_tokens_details.reasoning_tokens").Exists() {
		t.Fatalf("details missing: %s", u.Raw)
	}
}
