package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiResponsesNonStream_PromptBlockIsContentFilter(t *testing.T) {
	raw := []byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":4,"totalTokenCount":4},"modelVersion":"gemini-test","responseId":"resp_block"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, raw, nil)
	if gjson.GetBytes(out, "status").String() != "incomplete" {
		t.Fatalf("status = %s", out)
	}
	if gjson.GetBytes(out, "incomplete_details.reason").String() != "content_filter" {
		t.Fatalf("reason = %s", out)
	}
	if gjson.GetBytes(out, "incomplete_details.block_reason").String() != "PROHIBITED_CONTENT" {
		t.Fatalf("block_reason = %s", out)
	}
	text := gjson.GetBytes(out, "output.#.content.#.text").String()
	if text == "" {
		text = string(out)
	}
	if !strings.Contains(string(out), "PROHIBITED_CONTENT") || strings.Contains(strings.ToLower(string(out)), "account restriction") {
		t.Fatalf("output = %s", out)
	}
}

func TestGeminiResponsesStream_SafetyFinishAndMalformed(t *testing.T) {
	ctx := context.Background()
	var param any
	safety := []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":0,"totalTokenCount":2},"responseId":"resp_safety"}`)
	events := ConvertGeminiResponseToOpenAIResponses(ctx, "gemini-test", nil, nil, safety, &param)
	events = append(events, ConvertGeminiResponseToOpenAIResponses(ctx, "gemini-test", nil, nil, []byte("[DONE]"), &param)...)
	joined := string(bytesJoin(events))
	if !strings.Contains(joined, `"reason":"content_filter"`) || !strings.Contains(joined, `"block_reason":"SAFETY"`) {
		t.Fatalf("safety stream = %s", joined)
	}
	if !strings.Contains(joined, "response.incomplete") && !strings.Contains(joined, `"status":"incomplete"`) {
		t.Fatalf("safety stream did not terminate incomplete: %s", joined)
	}

	var malformedParam any
	malformed := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3},"responseId":"resp_bad"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, "gemini-test", nil, nil, malformed, &malformedParam)
	if gjson.GetBytes(out, "status").String() != "completed" {
		t.Fatalf("malformed status = %s", out)
	}
	if gjson.GetBytes(out, "incomplete_details.reason").String() == "content_filter" {
		t.Fatalf("malformed mapped to content_filter: %s", out)
	}
}

func TestGeminiResponsesNonStream_WrappedPromptBlock(t *testing.T) {
	raw := []byte(`{"response":{"promptFeedback":{"blockReason":"OTHER","blockReasonMessage":"blocked by policy"},"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1},"modelVersion":"gemini-test","responseId":"resp_other"}}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, raw, nil)
	if gjson.GetBytes(out, "incomplete_details.reason").String() != "content_filter" {
		t.Fatalf("status payload = %s", out)
	}
	if gjson.GetBytes(out, "incomplete_details.block_reason").String() != "OTHER" {
		t.Fatalf("block_reason = %s", out)
	}
	if !strings.Contains(string(out), "blockReason: OTHER") {
		t.Fatalf("missing explanation: %s", out)
	}
}

func bytesJoin(chunks [][]byte) []byte {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}
