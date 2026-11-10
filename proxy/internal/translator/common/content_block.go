package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	contentBlockReasonMax  = 64
	contentBlockMessageMax = 240
)

// ContentBlock is a per-request Google content-safety stop. It is not an
// account restriction and must not be described as one.
type ContentBlock struct {
	// Reason is promptFeedback.blockReason (PROHIBITED_CONTENT, SAFETY, OTHER, ...).
	Reason string
	// Message is the upstream blockReasonMessage, bounded.
	Message string
	// FinishReason is a safety-class candidate finishReason, uppercased.
	FinishReason string
}

// IsSafetyFinishReason reports candidate finish reasons that are content
// filters. MALFORMED_RESPONSE and MALFORMED_FUNCTION_CALL are intentionally
// excluded: they are transient generation failures, and reporting them as a
// Claude refusal ends the client session instead of allowing a retry.
func IsSafetyFinishReason(reason string) bool {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "IMAGE_SAFETY":
		return true
	default:
		return false
	}
}

// Active reports a promptFeedback block or a safety-class finish reason.
func (b ContentBlock) Active() bool {
	return strings.TrimSpace(b.Reason) != "" || IsSafetyFinishReason(b.FinishReason)
}

// UsageReason is the code stored on a usage record. A prompt block wins over
// the candidate finish reason.
func (b ContentBlock) UsageReason() string {
	if reason := strings.TrimSpace(b.Reason); reason != "" {
		return reason
	}
	if IsSafetyFinishReason(b.FinishReason) {
		return strings.ToUpper(strings.TrimSpace(b.FinishReason))
	}
	return ""
}

// Text is a short client-visible explanation. It names the upstream code and
// never claims the account is banned.
func (b ContentBlock) Text() string {
	if !b.Active() {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("Upstream content safety blocked this request")
	switch {
	case strings.TrimSpace(b.Reason) != "":
		buf.WriteString(" (blockReason: ")
		buf.WriteString(strings.TrimSpace(b.Reason))
		buf.WriteString(")")
	case IsSafetyFinishReason(b.FinishReason):
		buf.WriteString(" (finishReason: ")
		buf.WriteString(strings.ToUpper(strings.TrimSpace(b.FinishReason)))
		buf.WriteString(")")
	}
	buf.WriteString(".")
	if msg := strings.TrimSpace(b.Message); msg != "" {
		buf.WriteByte(' ')
		buf.WriteString(msg)
	}
	return buf.String()
}

// UsageBody is a small JSON document for the usage fail body.
func (b ContentBlock) UsageBody() string {
	body := []byte(`{"type":"content_block"}`)
	if reason := strings.TrimSpace(b.Reason); reason != "" {
		body, _ = sjson.SetBytes(body, "block_reason", reason)
	}
	if IsSafetyFinishReason(b.FinishReason) {
		body, _ = sjson.SetBytes(body, "finish_reason", strings.ToUpper(strings.TrimSpace(b.FinishReason)))
	}
	if msg := strings.TrimSpace(b.Message); msg != "" {
		body, _ = sjson.SetBytes(body, "block_reason_message", msg)
	}
	return string(body)
}

// ResponsesIncompleteDetails is the OpenAI Responses incomplete_details object
// for a content filter, plus the bounded upstream code.
func (b ContentBlock) ResponsesIncompleteDetails() []byte {
	out := []byte(`{"reason":"content_filter"}`)
	if reason := b.UsageReason(); reason != "" {
		out, _ = sjson.SetBytes(out, "block_reason", reason)
	}
	if msg := strings.TrimSpace(b.Message); msg != "" {
		out, _ = sjson.SetBytes(out, "block_reason_message", msg)
	}
	return out
}

// ReadContentBlock reads a promptFeedback block and/or a safety finishReason.
// Both the Gemini shape and the Antigravity {"response": ...} envelope are accepted.
func ReadContentBlock(raw []byte) (ContentBlock, bool) {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return ContentBlock{}, false
	}
	var out ContentBlock
	for _, prefix := range []string{"", "response."} {
		if out.Reason == "" {
			if reason := boundToken(gjson.GetBytes(raw, prefix+"promptFeedback.blockReason").String(), contentBlockReasonMax); reason != "" {
				out.Reason = reason
				out.Message = boundToken(gjson.GetBytes(raw, prefix+"promptFeedback.blockReasonMessage").String(), contentBlockMessageMax)
			}
		}
		if out.FinishReason == "" {
			if finish := strings.ToUpper(boundToken(gjson.GetBytes(raw, prefix+"candidates.0.finishReason").String(), contentBlockReasonMax)); IsSafetyFinishReason(finish) {
				out.FinishReason = finish
			}
		}
	}
	if !out.Active() {
		return ContentBlock{}, false
	}
	return out, true
}

// AttachPromptBlockCandidate gives a candidate-less promptFeedback block a
// single text part so translated pipelines that only walk candidates still
// emit the block reason. Payloads that already have a candidate are unchanged,
// and the promptFeedback object is left in place.
func AttachPromptBlockCandidate(raw []byte) []byte {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return raw
	}
	block, ok := ReadContentBlock(raw)
	if !ok || block.Reason == "" || contentBlockCandidateCount(raw) > 0 {
		return raw
	}
	candidate := []byte(`{"content":{"role":"model","parts":[{"text":""}]},"finishReason":""}`)
	candidate, _ = sjson.SetBytes(candidate, "content.parts.0.text", block.Text())
	candidate, _ = sjson.SetBytes(candidate, "finishReason", block.Reason)
	wrapped := append(append([]byte{'['}, candidate...), ']')
	path := "candidates"
	if gjson.GetBytes(raw, "response").Exists() && !gjson.GetBytes(raw, "candidates").Exists() {
		path = "response.candidates"
	}
	out, err := sjson.SetRawBytes(raw, path, wrapped)
	if err != nil {
		return raw
	}
	return out
}

// ClaudeStopReason maps an Antigravity/Gemini finish to a Claude stop_reason.
// Tool use wins. MALFORMED_* stays end_turn (see IsSafetyFinishReason).
func ClaudeStopReason(finishReason string, hasToolUse bool, blockReason string) string {
	if hasToolUse {
		return "tool_use"
	}
	switch strings.ToUpper(strings.TrimSpace(finishReason)) {
	case "MAX_TOKENS":
		return "max_tokens"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "IMAGE_SAFETY":
		return "refusal"
	}
	if strings.TrimSpace(blockReason) != "" {
		return "refusal"
	}
	return "end_turn"
}

// OpenAIChatFinishReason maps an upstream finish to OpenAI finish_reason.
// native is the raw upstream code, lowercased, and is never rewritten to the
// OpenAI vocabulary. Tool calls win over a content filter.
func OpenAIChatFinishReason(upstream string, sawTool bool, blockReason string) (finish, native string) {
	raw := strings.TrimSpace(upstream)
	if raw == "" {
		raw = strings.TrimSpace(blockReason)
	}
	native = "stop"
	if raw != "" {
		native = strings.ToLower(raw)
	}
	switch {
	case sawTool:
		finish = "tool_calls"
	case strings.EqualFold(strings.TrimSpace(upstream), "MAX_TOKENS"):
		finish = "length"
	case IsSafetyFinishReason(upstream) || strings.TrimSpace(blockReason) != "":
		finish = "content_filter"
	default:
		finish = "stop"
	}
	return finish, native
}

func contentBlockCandidateCount(raw []byte) int {
	count := 0
	for _, path := range []string{"candidates", "response.candidates"} {
		candidates := gjson.GetBytes(raw, path)
		if candidates.IsArray() && len(candidates.Array()) > count {
			count = len(candidates.Array())
		}
	}
	return count
}

func boundToken(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
