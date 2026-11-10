package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToClaude_CandidateLessBlock(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-test"}`)
	raw := []byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":6,"totalTokenCount":6},"modelVersion":"gemini-test","responseId":"resp-block"}`)
	out := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-test", requestJSON, requestJSON, raw, nil)
	if gjson.GetBytes(out, "stop_reason").String() != "refusal" {
		t.Fatalf("stop_reason payload = %s", out)
	}
	text := gjson.GetBytes(out, "content.0.text").String()
	if !strings.Contains(text, "PROHIBITED_CONTENT") || strings.Contains(strings.ToLower(text), "account") {
		t.Fatalf("text = %q", text)
	}

	var param any
	ctx := context.Background()
	stream := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-test", requestJSON, requestJSON, raw, &param), nil)
	stream = append(stream, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-test", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	body := string(stream)
	if !strings.Contains(body, `"stop_reason":"refusal"`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream did not terminate as a refusal:\n%s", body)
	}
}

func TestConvertGeminiResponseToClaude_MalformedFunctionCallStaysRefusal(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-test"}`)
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4},"modelVersion":"gemini-test","responseId":"resp-malformed"}`)
	out := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-test", requestJSON, requestJSON, raw, nil)
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "refusal" {
		t.Fatalf("stop_reason = %q, want the existing refusal mapping: %s", got, out)
	}
}
