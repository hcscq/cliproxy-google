package executor

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sensitivePatternsRuleName is appended to the prompt_rewritten note when at
// least one antigravity.sensitive-patterns-replace rule matched. Only the
// rule-set name (and the match count in debug logs) is recorded; the system
// text itself is never logged.
const sensitivePatternsRuleName = "sensitive-patterns-replace"

type compiledSensitivePattern struct {
	re          *regexp.Regexp
	replacement string
}

// sensitivePatternCache holds the compiled form of the most recently seen rule
// set so a request does not recompile regexes. Config reloads produce a new key
// and replace the entry.
var sensitivePatternCache struct {
	mu       sync.Mutex
	key      string
	compiled []compiledSensitivePattern
}

func sensitivePatternsKey(rules []config.SensitivePatternRule) string {
	var b strings.Builder
	for _, r := range rules {
		b.WriteString(r.Pattern)
		b.WriteByte(0)
		b.WriteString(r.Replacement)
		b.WriteByte(1)
	}
	return b.String()
}

// compileSensitivePatterns returns the compiled rules (invalid patterns are
// skipped; config load already drops them, this is defence in depth).
func compileSensitivePatterns(rules []config.SensitivePatternRule) []compiledSensitivePattern {
	if len(rules) == 0 {
		return nil
	}
	key := sensitivePatternsKey(rules)
	sensitivePatternCache.mu.Lock()
	defer sensitivePatternCache.mu.Unlock()
	if sensitivePatternCache.key == key && sensitivePatternCache.compiled != nil {
		return sensitivePatternCache.compiled
	}
	out := make([]compiledSensitivePattern, 0, len(rules))
	for _, r := range rules {
		if strings.TrimSpace(r.Pattern) == "" {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		out = append(out, compiledSensitivePattern{re: re, replacement: r.Replacement})
	}
	sensitivePatternCache.key = key
	sensitivePatternCache.compiled = out
	return out
}

// applySensitivePatternsToText applies the ordered rules to text without
// cascading: every rule matches against the ORIGINAL text, a match that
// overlaps a span already claimed by an earlier rule (or an earlier match of
// the same rule) is skipped, and replacements are spliced in one pass. Empty
// matches are ignored so a pattern like `x*` cannot insert text everywhere.
// Returns the new text and the number of replaced matches.
func applySensitivePatternsToText(text string, rules []compiledSensitivePattern) (string, int) {
	if text == "" || len(rules) == 0 {
		return text, 0
	}
	type span struct {
		start, end int
		repl       string
	}
	var spans []span
	overlaps := func(s, e int) bool {
		for _, sp := range spans {
			if s < sp.end && sp.start < e {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			if m[1] <= m[0] || overlaps(m[0], m[1]) {
				continue
			}
			repl := string(r.re.ExpandString(nil, r.replacement, text, m))
			spans = append(spans, span{start: m[0], end: m[1], repl: repl})
		}
	}
	if len(spans) == 0 {
		return text, 0
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, sp := range spans {
		b.WriteString(text[last:sp.start])
		b.WriteString(sp.repl)
		last = sp.end
	}
	b.WriteString(text[last:])
	return b.String(), len(spans)
}

// applyAntigravitySensitivePatterns rewrites request.systemInstruction /
// request.system_instruction text parts only. contents, tools and user turns
// are never touched. Parts left empty by a delete rule are dropped, and an
// instruction with no parts left is removed. Returns the payload and the total
// number of replaced matches.
func applyAntigravitySensitivePatterns(payload []byte, rules []config.SensitivePatternRule) ([]byte, int) {
	if len(payload) == 0 || len(rules) == 0 {
		return payload, 0
	}
	compiled := compileSensitivePatterns(rules)
	if len(compiled) == 0 {
		return payload, 0
	}
	total := 0
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		parts := gjson.GetBytes(payload, path+".parts")
		if !parts.IsArray() {
			continue
		}
		changed := false
		kept := []byte(`[]`)
		for _, part := range parts.Array() {
			text := part.Get("text")
			if text.Type != gjson.String {
				kept, _ = sjson.SetRawBytes(kept, "-1", []byte(part.Raw))
				continue
			}
			orig := text.String()
			updated, n := applySensitivePatternsToText(orig, compiled)
			if n == 0 || updated == orig {
				kept, _ = sjson.SetRawBytes(kept, "-1", []byte(part.Raw))
				continue
			}
			total += n
			changed = true
			if strings.TrimSpace(updated) == "" {
				continue
			}
			np, _ := sjson.SetBytes([]byte(part.Raw), "text", updated)
			kept, _ = sjson.SetRawBytes(kept, "-1", np)
		}
		if !changed {
			continue
		}
		if gjson.ParseBytes(kept).Get("#").Int() == 0 {
			payload, _ = sjson.DeleteBytes(payload, path)
		} else {
			payload, _ = sjson.SetRawBytes(payload, path+".parts", kept)
		}
	}
	return payload, total
}
