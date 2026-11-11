package executor

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CPA_ANTIGRAVITY_GENERIC_OBFUSCATE disables the default generic per-subword
// U+034F pass when set to 0, false, off, or no. Any other value (including
// unset) leaves the pass on. It only rewrites Antigravity systemInstruction
// text; contents, tools, and user turns are never touched.
const genericObfuscateEnv = "CPA_ANTIGRAVITY_GENERIC_OBFUSCATE"

// genericObfuscateRuleName is appended to prompt_rewritten when the generic
// pass inserts at least one U+034F. It is the only on-disk/log breadcrumb;
// the system text itself is not logged.
const genericObfuscateRuleName = "generic-u034f"

func genericObfuscateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(genericObfuscateEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// isGenericSubwordRune reports runes that belong to a generic subword:
// Latin letters, decimal digits, and already-inserted invisibles (so a
// previously obfuscated word is not split and is skipped as a whole).
// CJK letters are intentionally excluded; those stay the responsibility of
// the exact phrase rules unless a later, approved Chinese supplement is added.
func isGenericSubwordRune(r rune) bool {
	if r == '\u034F' || r == '\u200B' {
		return true
	}
	if unicode.IsDigit(r) {
		return true
	}
	return unicode.Is(unicode.Latin, r)
}

// obfuscateOneLatinSubword inserts one U+034F after the first rune of a
// subword that has at least two runes and does not already contain U+034F.
func obfuscateOneLatinSubword(subword string) (string, bool) {
	if strings.Contains(subword, cgj) {
		return subword, false
	}
	r, size := utf8.DecodeRuneInString(subword)
	if r == utf8.RuneError || size >= len(subword) {
		return subword, false
	}
	return string(r) + cgj + subword[size:], true
}

// obfuscateLatinSubwords inserts one U+034F after the first rune of every
// Latin-or-digit subword (>= 2 runes) in text. Punctuation, whitespace,
// CJK, and JSON/code structural characters are left untouched. The pass is
// deterministic and idempotent: a second call inserts nothing. n is the
// number of subwords newly marked.
//
// Unlike the official CLIProxyAPI v8.0.65 helper, this does not return
// early when the text already contains U+034F, and it does not fall back to
// marking the first rune of the whole string. Existing marks (from exact
// phrase rules) are skipped per-subword; remaining unmarked subwords are
// still processed.
func obfuscateLatinSubwords(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	var b strings.Builder
	b.Grow(len(text) + 8)
	subStart := -1
	n := 0
	for i, r := range text {
		if isGenericSubwordRune(r) {
			if subStart < 0 {
				subStart = i
			}
			continue
		}
		if subStart >= 0 {
			obf, did := obfuscateOneLatinSubword(text[subStart:i])
			b.WriteString(obf)
			if did {
				n++
			}
			subStart = -1
		}
		b.WriteRune(r)
	}
	if subStart >= 0 {
		obf, did := obfuscateOneLatinSubword(text[subStart:])
		b.WriteString(obf)
		if did {
			n++
		}
	}
	if n == 0 {
		return text, 0
	}
	return b.String(), n
}

// applyGenericSubwordObfuscate rewrites systemInstruction / system_instruction
// text parts only. It returns the (possibly updated) payload, whether any
// part changed, and the total number of subwords marked.
func applyGenericSubwordObfuscate(payload []byte) ([]byte, bool, int) {
	if len(payload) == 0 {
		return payload, false, 0
	}
	any := false
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
			updated, n := obfuscateLatinSubwords(orig)
			total += n
			if n == 0 || updated == orig {
				kept, _ = sjson.SetRawBytes(kept, "-1", []byte(part.Raw))
				continue
			}
			changed = true
			np, _ := sjson.SetBytes([]byte(part.Raw), "text", updated)
			kept, _ = sjson.SetRawBytes(kept, "-1", np)
		}
		if !changed {
			continue
		}
		any = true
		payload, _ = sjson.SetRawBytes(payload, path+".parts", kept)
	}
	return payload, any, total
}
