package gemini

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityGeminiPassthroughKeepsPromptFeedback(t *testing.T) {
	raw := []byte(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":9,"totalTokenCount":9},"modelVersion":"gemini-test","responseId":"resp-native"}}`)
	out := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "gemini-test", nil, nil, raw, nil)
	if gjson.GetBytes(out, "promptFeedback.blockReason").String() != "PROHIBITED_CONTENT" {
		t.Fatalf("non-stream dropped promptFeedback: %s", out)
	}
	if gjson.GetBytes(out, "promptFeedback.blockReasonMessage").String() != "The prompt is blocked due to safety" {
		t.Fatalf("message changed: %s", out)
	}
	if gjson.GetBytes(out, "candidates").Exists() {
		t.Fatalf("non-stream invented candidates: %s", out)
	}

	var param any
	ctx := context.WithValue(context.Background(), "alt", "")
	first := ConvertAntigravityResponseToGemini(ctx, "gemini-test", nil, nil, raw, &param)
	done := ConvertAntigravityResponseToGemini(ctx, "gemini-test", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 0 {
		t.Fatalf("stream synthesized a terminal chunk: %s", done[0])
	}
	if len(first) != 1 {
		t.Fatalf("stream chunks = %d", len(first))
	}
	joined := bytes.Join(first, nil)
	if gjson.GetBytes(joined, "promptFeedback.blockReason").String() != "PROHIBITED_CONTENT" {
		t.Fatalf("stream payload = %s", joined)
	}
	if strings.Contains(string(joined), `"finishReason":"STOP"`) {
		t.Fatalf("stream invented STOP: %s", joined)
	}
}
