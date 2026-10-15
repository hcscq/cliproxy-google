package executor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// Diagnostic sampling of successful-but-empty upstream replies (0 output and
// 0 reasoning tokens). Samples go to the Home KV with a TTL; they hold a
// request *shape* summary and a truncated response body, never prompt text.
const (
	emptySampleKeyPrefix = "diag:empty-sample:"
	emptySampleTTL       = 72 * time.Hour
	emptySampleBodyMax   = 4096
	emptySampleMinGap    = 2 * time.Second
)

var emptySampler = &emptySampleLimiter{}

type emptySampleLimiter struct {
	mu        sync.Mutex
	hour      time.Time
	count     int
	lastTaken time.Time
}

func emptySampleEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ANTIGRAVITY_EMPTY_SAMPLE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func emptySamplePerHour() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ANTIGRAVITY_EMPTY_SAMPLE_PER_HOUR"))); err == nil && n >= 0 {
		return n
	}
	return 120
}

func (l *emptySampleLimiter) allow(now time.Time, perHour int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := now.Truncate(time.Hour)
	if !h.Equal(l.hour) {
		l.hour, l.count = h, 0
	}
	if l.count >= perHour || now.Sub(l.lastTaken) < emptySampleMinGap {
		return false
	}
	l.count++
	l.lastTaken = now
	return true
}

// antigravityRequestShape summarizes request structure without prompt content.
func antigravityRequestShape(payload []byte) map[string]any {
	root := gjson.ParseBytes(payload)
	if r := root.Get("request"); r.Exists() && r.IsObject() {
		root = r
	}
	gc := root.Get("generationConfig")
	contents := root.Get("contents").Array()
	shape := map[string]any{
		"contents":            len(contents),
		"tools":               len(root.Get("tools").Array()),
		"tool_config_mode":    root.Get("toolConfig.functionCallingConfig.mode").String(),
		"system_instruction":  root.Get("systemInstruction").Exists(),
		"thinking_config":     gc.Get("thinkingConfig").Raw,
		"max_output_tokens":   gc.Get("maxOutputTokens").Int(),
		"response_mime_type":  gc.Get("responseMimeType").String(),
		"has_response_schema": gc.Get("responseSchema").Exists() || gc.Get("responseJsonSchema").Exists(),
		"candidate_count":     gc.Get("candidateCount").Int(),
		"stop_sequences":      len(gc.Get("stopSequences").Array()),
		"temperature":         gc.Get("temperature").Raw,
		"safety_settings":     len(root.Get("safetySettings").Array()),
		"cached_content":      root.Get("cachedContent").Exists(),
		"request_bytes":       len(payload),
	}
	if n := len(contents); n > 0 {
		last := contents[n-1]
		shape["last_role"] = last.Get("role").String()
		var kinds []string
		last.Get("parts").ForEach(func(_, p gjson.Result) bool {
			p.ForEach(func(k, _ gjson.Result) bool {
				if k.String() != "thoughtSignature" {
					kinds = append(kinds, k.String())
				}
				return true
			})
			return true
		})
		shape["last_part_kinds"] = kinds
	}
	return shape
}

// maybeSampleEmptyResponse stores a diagnostic sample when the reply had no
// output; it never blocks the caller.
func maybeSampleEmptyResponse(ctx context.Context, kind, model, authID string, requestPayload, body []byte, detail usage.Detail) {
	if detail.OutputTokens != 0 || detail.ReasoningTokens != 0 || !emptySampleEnabled() {
		return
	}
	now := time.Now()
	if !emptySampler.allow(now, emptySamplePerHour()) {
		return
	}
	resp := gjson.ParseBytes(body)
	if r := resp.Get("response"); r.Exists() && r.IsObject() {
		resp = r
	}
	truncated := len(body) > emptySampleBodyMax
	if truncated {
		body = body[:emptySampleBodyMax]
	}
	sample := map[string]any{
		"at":              now.UTC().Format(time.RFC3339Nano),
		"kind":            kind,
		"model":           model,
		"auth_id":         authID,
		"input_tokens":    detail.InputTokens,
		"finish_reason":   resp.Get("candidates.0.finishReason").String(),
		"finish_message":  resp.Get("candidates.0.finishMessage").String(),
		"block_reason":    resp.Get("promptFeedback.blockReason").String(),
		"prompt_feedback": resp.Get("promptFeedback").Raw,
		"candidates":      len(resp.Get("candidates").Array()),
		"parts":           len(resp.Get("candidates.0.content.parts").Array()),
		"usage_metadata":  resp.Get("usageMetadata").Raw,
		"model_version":   resp.Get("modelVersion").String(),
		"request":         antigravityRequestShape(requestPayload),
		"body":            string(body),
		"body_truncated":  truncated,
	}
	key := fmt.Sprintf("%s%s:%d", emptySampleKeyPrefix, now.UTC().Format("20060102T150405.000"), now.UnixNano()%1000000)
	go func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		homekv.KVSetJSONBestEffort(c, key, sample, emptySampleTTL)
	}()
}

// lastSSEJSON returns the raw SSE text with the last JSON frame first-class:
// samples keep the whole (capped) raw text, but finishReason etc. are read
// from the final data frame.
func lastSSEJSON(raw []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "data:"))
		if gjson.Valid(l) {
			if len(lines) == 1 {
				return []byte(l)
			}
			return []byte(`{"response":` + responseOf(l) + `,"raw_sse":` + strconv.Quote(string(raw)) + `}`)
		}
	}
	return raw
}

func responseOf(frame string) string {
	if r := gjson.Get(frame, "response"); r.Exists() && r.IsObject() {
		return r.Raw
	}
	return frame
}
