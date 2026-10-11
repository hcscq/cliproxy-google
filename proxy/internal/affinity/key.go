// Package affinity derives the credential-affinity (sticky session) key of a
// request. The key is deterministic and computed without any per-pod state, so
// every proxy replica maps the same conversation to the same key; Home stores
// key -> credential bindings in Postgres.
package affinity

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

// Header names used to hand the key to Home with each dispatch.
const (
	HeaderKey       = "X-Cpg-Affinity-Key"
	HeaderSource    = "X-Cpg-Affinity-Source"
	HeaderRequestID = "X-Cpg-Request-Id"
)

// Key sources, in default priority order.
const (
	SourceHeader        = "header"
	SourcePromptCache   = "prompt-cache-key"
	SourceOpenAIUser    = "openai-user"
	SourceClaudeMeta    = "claude-metadata"
	SourceGeminiCache   = "gemini-cached-content"
	SourcePrefix        = "prefix"
	defaultPrefixMsgs   = 1
	defaultPrefixChars  = 8000
	defaultMinPrefixLen = 2000
)

// AllSources is the default (and full) source list.
var AllSources = []string{SourceHeader, SourcePromptCache, SourceOpenAIUser, SourceClaudeMeta, SourceGeminiCache, SourcePrefix}

var sessionHeaders = []string{"X-Session-Id", "Session-Id", "X-Claude-Code-Session-Id", "X-Conversation-Id", "X-Thread-Id"}

// Compute returns the affinity key and its source, or "" when sticky routing is
// off or no source applies.
func Compute(cfg config.StickySessionsConfig, headers http.Header, body []byte, model string) (string, string) {
	if !cfg.StickyEnabled() {
		return "", ""
	}
	sources := cfg.KeySources
	if len(sources) == 0 {
		sources = AllSources
	}
	root := gjson.ParseBytes(body)
	if req := root.Get("request"); req.IsObject() && !root.Get("contents").Exists() {
		root = req // Antigravity / CLI envelope
	}
	for _, src := range sources {
		value := ""
		switch strings.ToLower(strings.TrimSpace(src)) {
		case SourceHeader:
			for _, h := range sessionHeaders {
				if v := strings.TrimSpace(headers.Get(h)); v != "" && len(v) <= 256 {
					value = v
					break
				}
			}
		case SourcePromptCache:
			value = root.Get("prompt_cache_key").String()
		case SourceOpenAIUser:
			if !root.Get("system").Exists() { // Claude bodies have no "user" field; keep OpenAI only
				value = root.Get("user").String()
			}
		case SourceClaudeMeta:
			value = root.Get("metadata.user_id").String()
		case SourceGeminiCache:
			value = root.Get("cachedContent").String()
			if value == "" {
				value = root.Get("cached_content").String()
			}
		case SourcePrefix:
			value = promptPrefix(root, cfg)
		default:
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			return hashKey(src, model, value), src
		}
	}
	return "", ""
}

func hashKey(source, model, value string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + strings.ToLower(strings.TrimSpace(model)) + "\x00" + value))
	return hex.EncodeToString(sum[:20])
}

// promptPrefix returns system text + the first N non-system messages, capped.
func promptPrefix(root gjson.Result, cfg config.StickySessionsConfig) string {
	n := cfg.PrefixMessages
	if n <= 0 {
		n = defaultPrefixMsgs
	}
	maxChars := cfg.PrefixChars
	if maxChars <= 0 {
		maxChars = defaultPrefixChars
	}
	minChars := cfg.MinPrefixChars
	if minChars <= 0 {
		minChars = defaultMinPrefixLen
	}
	var b strings.Builder
	// System / instructions in every protocol shape.
	for _, path := range []string{"system", "instructions", "systemInstruction", "system_instruction"} {
		if v := root.Get(path); v.Exists() {
			b.WriteString(text(v))
			b.WriteByte('\n')
		}
	}
	taken := 0
	for _, path := range []string{"messages", "contents", "input"} {
		list := root.Get(path)
		if list.Type == gjson.String { // Responses API string input
			b.WriteString(list.String())
			taken = n
			break
		}
		if !list.IsArray() {
			continue
		}
		for _, msg := range list.Array() {
			role := strings.ToLower(msg.Get("role").String())
			if role == "system" || role == "developer" {
				b.WriteString(text(msg.Get("content")))
				b.WriteByte('\n')
				continue
			}
			if taken >= n {
				continue
			}
			b.WriteString(role + ":")
			if c := msg.Get("content"); c.Exists() {
				b.WriteString(text(c))
			} else {
				b.WriteString(text(msg.Get("parts")))
			}
			b.WriteByte('\n')
			taken++
		}
		break
	}
	s := b.String()
	if len(s) > maxChars {
		s = s[:maxChars]
	}
	if len(strings.TrimSpace(s)) < minChars {
		return ""
	}
	return s
}

// text flattens string / parts / content blocks to text; non-text parts are
// represented by a short digest so images still distinguish prompts.
func text(v gjson.Result) string {
	switch {
	case v.Type == gjson.String:
		return v.String()
	case v.IsArray():
		var b strings.Builder
		for _, p := range v.Array() {
			b.WriteString(text(p))
		}
		return b.String()
	case v.IsObject():
		if t := v.Get("text"); t.Exists() {
			return t.String()
		}
		if p := v.Get("parts"); p.Exists() {
			return text(p)
		}
		if c := v.Get("content"); c.Exists() {
			return text(c)
		}
		sum := sha256.Sum256([]byte(v.Raw))
		return "[" + hex.EncodeToString(sum[:6]) + "]"
	}
	return ""
}
