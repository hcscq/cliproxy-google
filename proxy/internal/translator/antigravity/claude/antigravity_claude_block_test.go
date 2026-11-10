package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertAntigravityResponseToClaudeNonStream_RefusalFinishReasons(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	testCases := []struct {
		finishReason string
		wantReason   string
		wantNotice   bool
	}{
		{finishReason: "SAFETY", wantReason: "refusal", wantNotice: true},
		{finishReason: "RECITATION", wantReason: "refusal", wantNotice: true},
		{finishReason: "PROHIBITED_CONTENT", wantReason: "refusal", wantNotice: true},
		{finishReason: "SPII", wantReason: "refusal", wantNotice: true},
		{finishReason: "BLOCKLIST", wantReason: "refusal", wantNotice: true},
		{finishReason: "IMAGE_SAFETY", wantReason: "refusal", wantNotice: true},
		{finishReason: "MALFORMED_RESPONSE", wantReason: "end_turn"},
		{finishReason: "MALFORMED_FUNCTION_CALL", wantReason: "end_turn"},
		{finishReason: "MAX_TOKENS", wantReason: "max_tokens"},
		{finishReason: "STOP", wantReason: "end_turn"},
	}

	ctx := context.Background()
	for _, tc := range testCases {
		t.Run(tc.finishReason, func(t *testing.T) {
			rawJSON := []byte(`{
				"response": {
					"candidates": [{
						"content": {"role": "model", "parts": []},
						"finishReason": "` + tc.finishReason + `"
					}],
					"usageMetadata": {"promptTokenCount": 8149, "totalTokenCount": 8149},
					"modelVersion": "claude-opus-5-5-high",
					"responseId": "req_vrtx_test"
				}
			}`)
			output := ConvertAntigravityResponseToClaudeNonStream(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, rawJSON, nil)
			if got := gjson.GetBytes(output, "stop_reason").String(); got != tc.wantReason {
				t.Fatalf("stop_reason = %q, want %q. Output: %s", got, tc.wantReason, output)
			}
			text := gjson.GetBytes(output, "content.0.text").String()
			if tc.wantNotice {
				if !strings.Contains(text, tc.finishReason) || strings.Contains(text, "account") {
					t.Fatalf("notice = %q", text)
				}
			} else if strings.Contains(text, "content safety") {
				t.Fatalf("unexpected notice for %s: %s", tc.finishReason, output)
			}
		})
	}

	t.Run("tool_use_takes_precedence_over_safety", func(t *testing.T) {
		origReq := []byte(`{"model":"claude-opus-5-5","tools":[{"name":"echo","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}}]}`)
		rawJSON := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": [{"functionCall": {"name": "echo", "args": {"x": "1"}}}]},
					"finishReason": "SAFETY"
				}],
				"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_vrtx_tool"
			}
		}`)
		output := ConvertAntigravityResponseToClaudeNonStream(ctx, "claude-opus-5-5-high", origReq, origReq, rawJSON, nil)
		if got := gjson.GetBytes(output, "stop_reason").String(); got != "tool_use" {
			t.Fatalf("stop_reason = %q, want tool_use. Output: %s", got, output)
		}
	})
}

func TestConvertAntigravityResponseToClaudeNonStream_PromptBlock(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5"}`)
	rawJSON := []byte(`{
		"response": {
			"promptFeedback": {"blockReason": "PROHIBITED_CONTENT", "blockReasonMessage": "The prompt is blocked due to safety"},
			"usageMetadata": {"promptTokenCount": 12, "totalTokenCount": 12},
			"modelVersion": "claude-opus-5-5-high",
			"responseId": "req_block"
		}
	}`)
	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, rawJSON, nil)
	if got := gjson.GetBytes(output, "stop_reason").String(); got != "refusal" {
		t.Fatalf("stop_reason = %q, output %s", got, output)
	}
	text := gjson.GetBytes(output, "content.0.text").String()
	if !strings.Contains(text, "PROHIBITED_CONTENT") || !strings.Contains(text, "blocked due to safety") {
		t.Fatalf("text = %q", text)
	}
	if strings.Contains(strings.ToLower(text), "account") || strings.Contains(text, "PUP") {
		t.Fatalf("text invents an account ban: %s", text)
	}
}

func TestConvertAntigravityResponseToClaudeStream_EmptySafetyRefusal(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5"}`)
	translatedRequestJSON := []byte(`{"model":"claude-opus-5-5-high"}`)
	firstChunk := []byte(`{
		"response": {
			"candidates": [{"content": {"role": "model", "parts": [{"text": ""}]}}],
			"modelVersion": "claude-opus-5-5-high",
			"responseId": "req_vrtx_011CgdVmG8o2DSoFQtSfzktZ",
			"cpaUsageMetadata": {"promptTokenCount": 8149, "totalTokenCount": 8149}
		}
	}`)
	terminalChunk := []byte(`{
		"response": {
			"candidates": [{"content": {"role": "model", "parts": [{"text": ""}]}, "finishReason": "SAFETY"}],
			"usageMetadata": {"promptTokenCount": 8149, "totalTokenCount": 8149},
			"modelVersion": "claude-opus-5-5-high",
			"responseId": "req_vrtx_011CgdVmG8o2DSoFQtSfzktZ"
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, translatedRequestJSON, firstChunk, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, translatedRequestJSON, terminalChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, translatedRequestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	lastIndex := -1
	for _, eventName := range []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"} {
		index := strings.Index(outputText, "event: "+eventName+"\n")
		if index < 0 || index <= lastIndex {
			t.Fatalf("event %q out of order in:\n%s", eventName, outputText)
		}
		lastIndex = index
	}
	messageDelta := sseDataForEvent(t, outputText, "message_delta")
	if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "refusal" {
		t.Fatalf("stop_reason = %q, want refusal: %s", got, messageDelta)
	}
	if !strings.Contains(outputText, "finishReason: SAFETY") {
		t.Fatalf("stream did not name the finish reason:\n%s", outputText)
	}
}

func TestConvertAntigravityResponseToClaudeStream_RefusalPreservedAcrossTrailingEmptyFinishReason(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5"}`)
	for _, reason := range []string{"SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "IMAGE_SAFETY"} {
		t.Run(reason, func(t *testing.T) {
			finishChunk := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"` + reason + `"}],"modelVersion":"claude-opus-5-5-high","responseId":"req_vrtx_split"}}`)
			usageChunk := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":""}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"totalTokenCount":101},"modelVersion":"claude-opus-5-5-high","responseId":"req_vrtx_split"}}`)
			var param any
			ctx := context.Background()
			output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, finishChunk, &param), nil)
			output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, usageChunk, &param), nil)...)
			output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
			messageDelta := sseDataForEvent(t, string(output), "message_delta")
			if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "refusal" {
				t.Fatalf("stop_reason = %q, want refusal: %s", got, messageDelta)
			}
		})
	}
	t.Run("MALFORMED_FUNCTION_CALL", func(t *testing.T) {
		finishChunk := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"totalTokenCount":101},"modelVersion":"claude-opus-5-5-high","responseId":"req_malformed"}}`)
		var param any
		ctx := context.Background()
		output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, finishChunk, &param), nil)
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
		messageDelta := sseDataForEvent(t, string(output), "message_delta")
		if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "end_turn" {
			t.Fatalf("stop_reason = %q, want end_turn: %s", got, messageDelta)
		}
	})
}

func TestConvertAntigravityResponseToClaudeStream_CandidateLessBlockTerminates(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5"}`)
	chunk := []byte(`{"response":{"promptFeedback":{"blockReason":"OTHER","blockReasonMessage":"blocked by policy"},"usageMetadata":{"promptTokenCount":4,"totalTokenCount":4},"modelVersion":"claude-opus-5-5-high","responseId":"req_other"}}`)
	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)
	for _, eventName := range []string{"message_start", "message_delta", "message_stop"} {
		if !strings.Contains(outputText, "event: "+eventName+"\n") {
			t.Fatalf("missing %s in:\n%s", eventName, outputText)
		}
	}
	messageDelta := sseDataForEvent(t, outputText, "message_delta")
	if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "refusal" {
		t.Fatalf("stop_reason = %q: %s", got, messageDelta)
	}
	if !strings.Contains(outputText, "blockReason: OTHER") || !strings.Contains(outputText, "blocked by policy") {
		t.Fatalf("block text missing:\n%s", outputText)
	}
}

func TestConvertAntigravityResponseToClaudeStream_WebSearchBufferedTextDrainedOnEmptyFinishReason(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)
	chunk := []byte(`{
		"response": {
			"modelVersion": "gemini-3.1-flash-lite",
			"responseId": "resp-web-search-empty-finish",
			"candidates": [{
				"content": {"parts": [{"text": "Direct answer without grounding."}]},
				"finishReason": ""
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 4, "totalTokenCount": 14}
		}
	}`)
	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)
	if !strings.Contains(outputText, "Direct answer without grounding.") {
		t.Fatalf("buffered web-search text was lost on empty finishReason:\n%s", outputText)
	}
	messageDelta := sseDataForEvent(t, outputText, "message_delta")
	if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, messageDelta)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_WebSearchGroundingRespectsFinishReason(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)
	for _, tc := range []struct {
		finishReason string
		wantReason   string
	}{
		{finishReason: "SAFETY", wantReason: "refusal"},
		{finishReason: "MAX_TOKENS", wantReason: "max_tokens"},
		{finishReason: "STOP", wantReason: "end_turn"},
	} {
		t.Run(tc.finishReason, func(t *testing.T) {
			rawJSON := []byte(`{
				"response": {
					"modelVersion": "gemini-3.1-flash-lite",
					"responseId": "resp-web-search-grounding-finish",
					"candidates": [{
						"content": {"parts": [{"text": "Partial grounded text."}]},
						"groundingMetadata": {
							"webSearchQueries": ["query"],
							"groundingChunks": [{"web": {"uri": "https://example.com", "title": "Example"}}]
						},
						"finishReason": "` + tc.finishReason + `"
					}],
					"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 4, "totalTokenCount": 14}
				}
			}`)
			output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, rawJSON, nil)
			if got := gjson.GetBytes(output, "stop_reason").String(); got != tc.wantReason {
				t.Fatalf("stop_reason = %q, want %q: %s", got, tc.wantReason, output)
			}
		})
	}
}
