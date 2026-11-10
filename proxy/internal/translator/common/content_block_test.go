package common

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestReadContentBlockPromptFeedbackAndSafetyFinish(t *testing.T) {
	wrapped := []byte(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":3}}}`)
	block, ok := ReadContentBlock(wrapped)
	if !ok || block.Reason != "PROHIBITED_CONTENT" || block.Message == "" {
		t.Fatalf("wrapped block = %+v ok=%v", block, ok)
	}
	if strings.Contains(strings.ToLower(block.Text()), "account") || strings.Contains(block.Text(), "PUP") {
		t.Fatalf("block text invents an account ban: %s", block.Text())
	}
	if !strings.Contains(block.Text(), "PROHIBITED_CONTENT") || !strings.Contains(block.Text(), "blocked due to safety") {
		t.Fatalf("block text = %q", block.Text())
	}

	finishOnly := []byte(`{"candidates":[{"finishReason":"SAFETY","content":{"parts":[]}}]}`)
	block, ok = ReadContentBlock(finishOnly)
	if !ok || block.FinishReason != "SAFETY" || block.Reason != "" {
		t.Fatalf("finish block = %+v ok=%v", block, ok)
	}

	for _, raw := range []string{
		`{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
		`{"candidates":[{"finishReason":"MALFORMED_RESPONSE"}]}`,
		`{"candidates":[{"finishReason":"STOP"}]}`,
		`{"candidates":[{"finishReason":"OTHER"}]}`,
		`{"response":{}}`,
	} {
		if _, ok := ReadContentBlock([]byte(raw)); ok {
			t.Fatalf("non-block %s was reported as a content block", raw)
		}
	}
}

func TestClaudeAndOpenAIFinishMappingKeepsMalformed(t *testing.T) {
	if got := ClaudeStopReason("PROHIBITED_CONTENT", false, ""); got != "refusal" {
		t.Fatalf("claude prohibited = %s", got)
	}
	if got := ClaudeStopReason("MALFORMED_FUNCTION_CALL", false, ""); got != "end_turn" {
		t.Fatalf("claude malformed = %s, want end_turn", got)
	}
	if got := ClaudeStopReason("SAFETY", true, "SAFETY"); got != "tool_use" {
		t.Fatalf("tool use = %s", got)
	}
	if got := ClaudeStopReason("", false, "OTHER"); got != "refusal" {
		t.Fatalf("block OTHER = %s", got)
	}

	finish, native := OpenAIChatFinishReason("PROHIBITED_CONTENT", false, "")
	if finish != "content_filter" || native != "prohibited_content" {
		t.Fatalf("openai safety = %s / %s", finish, native)
	}
	finish, native = OpenAIChatFinishReason("MALFORMED_FUNCTION_CALL", false, "")
	if finish != "stop" || native != "malformed_function_call" {
		t.Fatalf("openai malformed = %s / %s", finish, native)
	}
	finish, native = OpenAIChatFinishReason("SAFETY", true, "")
	if finish != "tool_calls" || native != "safety" {
		t.Fatalf("openai tool = %s / %s", finish, native)
	}
	finish, native = OpenAIChatFinishReason("", false, "OTHER")
	if finish != "content_filter" || native != "other" {
		t.Fatalf("openai block = %s / %s", finish, native)
	}
}

func TestAttachPromptBlockCandidateLeavesExistingCandidates(t *testing.T) {
	original := []byte(`{"response":{"promptFeedback":{"blockReason":"OTHER","blockReasonMessage":"blocked"},"usageMetadata":{"promptTokenCount":1}}}`)
	attached := AttachPromptBlockCandidate(original)
	if gjson.GetBytes(attached, "response.promptFeedback.blockReason").String() != "OTHER" {
		t.Fatalf("promptFeedback dropped: %s", attached)
	}
	if gjson.GetBytes(attached, "response.candidates.0.finishReason").String() != "OTHER" {
		t.Fatalf("synthetic finish = %s", attached)
	}
	if !strings.Contains(gjson.GetBytes(attached, "response.candidates.0.content.parts.0.text").String(), "blockReason: OTHER") {
		t.Fatalf("synthetic text = %s", attached)
	}

	withCandidate := []byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"promptFeedback":{"blockReason":"SAFETY"}}`)
	if got := AttachPromptBlockCandidate(withCandidate); string(got) != string(withCandidate) {
		t.Fatalf("existing candidate was rewritten: %s", got)
	}
}

func TestContentBlockUsageBodyIsBounded(t *testing.T) {
	long := strings.Repeat("x", 500)
	block, ok := ReadContentBlock([]byte(`{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"` + long + `"}}`))
	if !ok {
		t.Fatal("expected block")
	}
	if len(block.Message) > contentBlockMessageMax {
		t.Fatalf("message len = %d", len(block.Message))
	}
	body := block.UsageBody()
	if gjson.Get(body, "type").String() != "content_block" || gjson.Get(body, "block_reason").String() != "SAFETY" {
		t.Fatalf("usage body = %s", body)
	}
	if strings.Contains(body, long) {
		t.Fatal("usage body kept the unbounded message")
	}
}
