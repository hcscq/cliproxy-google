package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

// Upstream Antigravity streams for gemini-3.x models frequently omit thought
// summary parts (only thoughtsTokenCount and a trailing thoughtSignature are
// sent), while the non-stream endpoint always returns them. When
// routing.stream-thinking-emulation is on, streaming requests that ask for
// thoughts (includeThoughts=true) on Gemini models are sent to the non-stream
// endpoint and the complete response is replayed as one SSE event through the
// normal stream translators. Default off: it delays the first byte until the
// whole answer is ready.

// streamThinkingEmulationApplies reports whether the request should use the
// non-stream endpoint with SSE replay.
func streamThinkingEmulationApplies(cfg *config.Config, model string, payload []byte) bool {
	if cfg == nil || !cfg.StreamThinkingEmulation {
		return false
	}
	lower := strings.ToLower(model)
	if !strings.HasPrefix(lower, "gemini-") {
		return false
	}
	return gjson.GetBytes(payload, "request.generationConfig.thinkingConfig.includeThoughts").Bool()
}

// replayNonStreamAsSSE turns a non-stream Antigravity response body into a
// single compact "data:" SSE event.
func replayNonStreamAsSSE(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("stream thinking emulation: invalid upstream JSON: %w", err)
	}
	var out bytes.Buffer
	out.Grow(compact.Len() + 8)
	out.WriteString("data: ")
	out.Write(compact.Bytes())
	out.WriteString("\n\n")
	return out.Bytes(), nil
}
