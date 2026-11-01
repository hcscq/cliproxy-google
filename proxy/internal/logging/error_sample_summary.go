package logging

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// RequestErrorSummary is a compact, redacted structural view of a failing
// request body. It never includes message text, full signatures, or secrets.
type RequestErrorSummary struct {
	MessageCount       int                 `json:"message_count"`
	Roles              []string            `json:"roles,omitempty"`
	BlockTypes         map[string]int      `json:"block_types,omitempty"`
	ThinkingBlocks     int                 `json:"thinking_blocks,omitempty"`
	ThinkingSignatures []ThinkingSigMeta   `json:"thinking_signatures,omitempty"`
	ToolCallNames      []string            `json:"tool_call_names,omitempty"`
	ToolResponseNames  []string            `json:"tool_response_names,omitempty"`
	ApproxInputTokens  int                 `json:"approx_input_tokens,omitempty"`
	HasTools           bool                `json:"has_tools,omitempty"`
	Model              string              `json:"model,omitempty"`
	SessionHint        string              `json:"session_hint,omitempty"`
}

// ThinkingSigMeta records only length + short hash of a thinking signature.
type ThinkingSigMeta struct {
	Length int    `json:"length"`
	Hash8  string `json:"hash8,omitempty"`
}

// SummarizeRequestForErrorSample builds a structural summary of a Claude /
// OpenAI-style chat body, or a Gemini contents body. Safe for logging.
func SummarizeRequestForErrorSample(body []byte) RequestErrorSummary {
	sum := RequestErrorSummary{BlockTypes: map[string]int{}}
	if len(body) == 0 {
		return sum
	}
	sum.Model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	sum.SessionHint = firstNonEmpty(
		strings.TrimSpace(gjson.GetBytes(body, "session_id").String()),
		strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()),
	)
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		sum.HasTools = true
	}
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		summarizeMessages(msgs.Array(), &sum)
	} else if contents := gjson.GetBytes(body, "contents"); contents.IsArray() {
		summarizeGeminiContents(contents.Array(), &sum)
	} else if req := gjson.GetBytes(body, "request"); req.Exists() {
		if msgs := req.Get("contents"); msgs.IsArray() {
			summarizeGeminiContents(msgs.Array(), &sum)
		}
	}
	// Approx tokens: body bytes / 4, capped.
	approx := len(body) / 4
	if approx > 2_000_000 {
		approx = 2_000_000
	}
	sum.ApproxInputTokens = approx
	if len(sum.BlockTypes) == 0 {
		sum.BlockTypes = nil
	}
	sum.ToolCallNames = uniqueSorted(sum.ToolCallNames)
	sum.ToolResponseNames = uniqueSorted(sum.ToolResponseNames)
	return sum
}

func summarizeMessages(msgs []gjson.Result, sum *RequestErrorSummary) {
	sum.MessageCount = len(msgs)
	for _, m := range msgs {
		role := strings.TrimSpace(m.Get("role").String())
		if role == "" {
			role = "unknown"
		}
		sum.Roles = append(sum.Roles, role)
		content := m.Get("content")
		if content.Type == gjson.String {
			sum.BlockTypes["text"]++
			continue
		}
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			typ := strings.TrimSpace(part.Get("type").String())
			if typ == "" {
				typ = "unknown"
			}
			sum.BlockTypes[typ]++
			switch typ {
			case "thinking", "reasoning":
				sum.ThinkingBlocks++
				if sig := strings.TrimSpace(part.Get("signature").String()); sig != "" {
					sum.ThinkingSignatures = append(sum.ThinkingSignatures, thinkingSigMeta(sig))
				}
			case "tool_use", "function_call":
				if n := firstNonEmpty(part.Get("name").String(), part.Get("function.name").String()); n != "" {
					sum.ToolCallNames = append(sum.ToolCallNames, n)
				}
			case "tool_result", "function_response":
				if n := firstNonEmpty(part.Get("name").String(), part.Get("tool_use_id").String()); n != "" {
					sum.ToolResponseNames = append(sum.ToolResponseNames, n)
				}
			}
		}
	}
}

func summarizeGeminiContents(contents []gjson.Result, sum *RequestErrorSummary) {
	sum.MessageCount = len(contents)
	for _, c := range contents {
		role := strings.TrimSpace(c.Get("role").String())
		if role == "" {
			role = "unknown"
		}
		sum.Roles = append(sum.Roles, role)
		parts := c.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			switch {
			case part.Get("text").Exists():
				if part.Get("thought").Bool() || part.Get("thoughtSignature").Exists() {
					sum.BlockTypes["thought"]++
					sum.ThinkingBlocks++
					if sig := strings.TrimSpace(part.Get("thoughtSignature").String()); sig != "" {
						sum.ThinkingSignatures = append(sum.ThinkingSignatures, thinkingSigMeta(sig))
					}
				} else {
					sum.BlockTypes["text"]++
				}
			case part.Get("functionCall").Exists():
				sum.BlockTypes["functionCall"]++
				if n := part.Get("functionCall.name").String(); n != "" {
					sum.ToolCallNames = append(sum.ToolCallNames, n)
				}
			case part.Get("functionResponse").Exists():
				sum.BlockTypes["functionResponse"]++
				if n := part.Get("functionResponse.name").String(); n != "" {
					sum.ToolResponseNames = append(sum.ToolResponseNames, n)
				}
			default:
				sum.BlockTypes["other"]++
			}
			if sig := strings.TrimSpace(part.Get("thoughtSignature").String()); sig != "" && !part.Get("thought").Bool() && !part.Get("text").Exists() {
				sum.ThinkingSignatures = append(sum.ThinkingSignatures, thinkingSigMeta(sig))
			}
		}
	}
}

func thinkingSigMeta(sig string) ThinkingSigMeta {
	sum := sha256.Sum256([]byte(sig))
	return ThinkingSigMeta{Length: utf8.RuneCountInString(sig), Hash8: hex.EncodeToString(sum[:4])}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ExtractSessionHintFromRequest pulls a session id from headers or body.
func ExtractSessionHintFromRequest(headers map[string][]string, body []byte) string {
	for _, k := range []string{"X-Session-Id", "X-Session-ID", "x-session-id", "X-Claude-Code-Session-Id", "session_id"} {
		if vs := headers[k]; len(vs) > 0 {
			if s := strings.TrimSpace(vs[0]); s != "" {
				return truncateRunes(s, 64)
			}
		}
		// case-insensitive header scan
		for hk, vs := range headers {
			if strings.EqualFold(hk, k) && len(vs) > 0 {
				if s := strings.TrimSpace(vs[0]); s != "" {
					return truncateRunes(s, 64)
				}
			}
		}
	}
	if s := strings.TrimSpace(gjson.GetBytes(body, "session_id").String()); s != "" {
		return truncateRunes(s, 64)
	}
	if s := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); s != "" {
		return truncateRunes(s, 64)
	}
	return ""
}

// ExtractAuthIndexFromErrorTexts parses auth_file=<uuid> from conductor error text.
func ExtractAuthIndexFromErrorTexts(texts ...string) string {
	const marker = "auth_file="
	for _, t := range texts {
		if i := strings.Index(t, marker); i >= 0 {
			rest := t[i+len(marker):]
			end := len(rest)
			for j, r := range rest {
				if r == ' ' || r == ',' || r == ';' || r == ')' || r == '"' || r == '\n' || r == '\r' {
					end = j
					break
				}
			}
			id := strings.TrimSpace(rest[:end])
			if id != "" {
				return truncateRunes(id, 64)
			}
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}
