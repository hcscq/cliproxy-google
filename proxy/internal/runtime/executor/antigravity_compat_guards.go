package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// antigravityEstimateInputTokens approximates input size the same way TTFB
// scaling does (payload bytes / 4). Good enough for a local over-limit gate.
func antigravityEstimateInputTokens(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	return (len(payload) + 3) / 4
}

func antigravityModelInputLimit(modelName string, info *registry.ModelInfo) int {
	if info != nil {
		for _, n := range []int{info.InputTokenLimit, info.ContextLength, info.MaxContextLength} {
			if n > 0 {
				return n
			}
		}
	}
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(lower, "gemini-3.8-flash"), strings.Contains(lower, "gemini-3-8-flash"):
		return 131072
	case strings.Contains(lower, "gemini-3.7-flash"), strings.Contains(lower, "gemini-3-7-flash"),
		strings.Contains(lower, "gemini-3.6-flash"), strings.Contains(lower, "gemini-3-6-flash"),
		strings.Contains(lower, "gemini-3.5-flash"), strings.Contains(lower, "gemini-3-5-flash"),
		strings.Contains(lower, "gemini-3-flash"):
		return 1048576
	case strings.Contains(lower, "gemini-2.5-flash"):
		return 1048576
	case strings.Contains(lower, "gemini-2.5-pro"):
		return 1048576
	case strings.Contains(lower, "gemini-4-argon"), strings.Contains(lower, "gemini-4"):
		return 1048576
	default:
		return 0
	}
}

// antigravityContextPrecheckError returns a local 400 when the estimated input
// exceeds the model limit. nil means the request may proceed.
func antigravityContextPrecheckError(modelName string, info *registry.ModelInfo, payload []byte) error {
	if !antigravityContextTokenPrecheckEnabled() {
		return nil
	}
	limit := antigravityModelInputLimit(modelName, info)
	if limit <= 0 {
		return nil
	}
	estimated := antigravityEstimateInputTokens(payload)
	// bytes/4 under-estimates some tokenizers; only reject when clearly over a margin.
	clearOver := estimated > limit+(limit/20) || estimated-limit > 2048
	if estimated <= limit || !clearOver {
		return nil
	}
	msg := fmt.Sprintf(
		"context length exceeded: estimated %d input tokens > model limit %d for %s (local precheck; request was not forwarded upstream). Shorten the prompt or use a model with a larger context window.",
		estimated, limit, strings.TrimSpace(modelName),
	)
	if antigravityContextTokenPrecheckObserveOnly() {
		log.Warnf("antigravity: observe %s (mode=observe; not applied)", msg)
		return nil
	}
	log.Warnf("antigravity: %s", msg)
	return statusErr{code: http.StatusBadRequest, msg: msg}
}

func antigravityInvalidThinkingSignatureBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "thinking") {
		return false
	}
	return strings.Contains(lower, "invalid") && strings.Contains(lower, "signature")
}

// stripAntigravitySignedThinkingForRetry removes signature-bearing thinking /
// thought parts so a foreign credential signature cannot be replayed. Used only
// as a one-shot recovery after upstream 400 Invalid signature in thinking block.
func stripAntigravitySignedThinkingForRetry(payload []byte) ([]byte, int) {
	if len(payload) == 0 {
		return payload, 0
	}
	out := payload
	removed := 0

	if messages := util.GetGJSONBytesNoCopy(out, "messages"); messages.IsArray() {
		changed := false
		msgItems := make([][]byte, 0, len(messages.Array()))
		for _, message := range messages.Array() {
			msgJSON := []byte(message.Raw)
			content := message.Get("content")
			if !content.IsArray() {
				msgItems = append(msgItems, msgJSON)
				continue
			}
			kept := make([][]byte, 0, len(content.Array()))
			contentChanged := false
			for _, block := range content.Array() {
				typ := block.Get("type").String()
				sig := strings.TrimSpace(block.Get("signature").String())
				if (typ == "thinking" || typ == "redacted_thinking") && sig != "" {
					contentChanged = true
					removed++
					continue
				}
				kept = append(kept, []byte(block.Raw))
			}
			if contentChanged {
				msgJSON, _ = sjson.SetRawBytes(msgJSON, "content", translatorcommon.JoinRawArray(kept))
				changed = true
			}
			msgItems = append(msgItems, msgJSON)
		}
		if changed {
			if updated, err := sjson.SetRawBytes(out, "messages", translatorcommon.JoinRawArray(msgItems)); err == nil {
				out = updated
			}
		}
	}

	for _, path := range []string{"request.contents", "contents"} {
		contents := util.GetGJSONBytesNoCopy(out, path)
		if !contents.IsArray() {
			continue
		}
		changed := false
		contentItems := make([][]byte, 0, len(contents.Array()))
		for _, content := range contents.Array() {
			contentJSON := []byte(content.Raw)
			parts := content.Get("parts")
			if !parts.IsArray() {
				contentItems = append(contentItems, contentJSON)
				continue
			}
			kept := make([][]byte, 0, len(parts.Array()))
			partsChanged := false
			for _, part := range parts.Array() {
				sig := strings.TrimSpace(part.Get("thoughtSignature").String())
				if sig == "" {
					sig = strings.TrimSpace(part.Get("thought_signature").String())
				}
				hasTool := part.Get("functionCall").Exists() || part.Get("function_call").Exists() ||
					part.Get("functionResponse").Exists() || part.Get("function_response").Exists()
				thought := part.Get("thought").Bool()
				if sig == "" {
					kept = append(kept, []byte(part.Raw))
					continue
				}
				if hasTool {
					updatedPart := []byte(part.Raw)
					updatedPart, _ = sjson.DeleteBytes(updatedPart, "thoughtSignature")
					updatedPart, _ = sjson.DeleteBytes(updatedPart, "thought_signature")
					kept = append(kept, updatedPart)
					partsChanged = true
					removed++
					continue
				}
				// Drop thinking / signature-only carriers.
				if thought || !part.Get("text").Exists() || strings.TrimSpace(part.Get("text").String()) == "" {
					partsChanged = true
					removed++
					continue
				}
				updatedPart := []byte(part.Raw)
				updatedPart, _ = sjson.DeleteBytes(updatedPart, "thoughtSignature")
				updatedPart, _ = sjson.DeleteBytes(updatedPart, "thought_signature")
				kept = append(kept, updatedPart)
				partsChanged = true
				removed++
			}
			if partsChanged {
				if len(kept) == 0 {
					changed = true
					continue
				}
				contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts", translatorcommon.JoinRawArray(kept))
				changed = true
			}
			contentItems = append(contentItems, contentJSON)
		}
		if changed {
			if updated, err := sjson.SetRawBytes(out, path, translatorcommon.JoinRawArray(contentItems)); err == nil {
				out = updated
			}
		}
	}
	return out, removed
}
