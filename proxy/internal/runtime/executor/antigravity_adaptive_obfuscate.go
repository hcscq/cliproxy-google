package executor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	adaptiveMaxLevel = 2
	// L1 inserts a CGJ after the first CJK rune of a run and then every
	// adaptiveCJKStride visible CJK runes. L2 uses stride 1 (逐字).
	adaptiveCJKStrideL1 = 2
)

func isCJKLetter(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// obfuscateCJKRuns inserts U+034F after selected runes of each consecutive
// CJK letter run (punctuation and latin break a run). stride 1 marks every
// CJK letter (skipping a rune that is already followed by U+034F); stride 2
// marks the first and then every second letter. Idempotent and deterministic.
func obfuscateCJKRuns(text string, stride int) (string, int) {
	if text == "" || stride < 1 {
		return text, 0
	}
	type span struct{ start, end int } // byte offsets of a CJK run
	var runs []span
	start := -1
	for i, r := range text {
		// Existing CGJ / ZWSP after a CJK letter stays inside the run so a
		// later pass can see the mark and stay idempotent.
		if isCJKLetter(r) || (start >= 0 && (r == '͏' || r == '​')) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			runs = append(runs, span{start, i})
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, span{start, len(text)})
	}
	if len(runs) == 0 {
		return text, 0
	}
	var b strings.Builder
	b.Grow(len(text) + 16)
	cursor := 0
	n := 0
	for _, run := range runs {
		b.WriteString(text[cursor:run.start])
		seg := text[run.start:run.end]
		out, k := markCJKRun(seg, stride)
		b.WriteString(out)
		n += k
		cursor = run.end
	}
	b.WriteString(text[cursor:])
	if n == 0 {
		return text, 0
	}
	return b.String(), n
}

func markCJKRun(seg string, stride int) (string, int) {
	if seg == "" {
		return seg, 0
	}
	var b strings.Builder
	b.Grow(len(seg) + 8)
	visible := 0
	n := 0
	for i, r := range seg {
		size := utf8.RuneLen(r)
		if size <= 0 {
			size = 1
		}
		b.WriteString(seg[i : i+size])
		if !isCJKLetter(r) {
			continue
		}
		// already followed by a CGJ (possibly from a previous pass)
		next := i + size
		if next < len(seg) {
			nr, _ := utf8.DecodeRuneInString(seg[next:])
			if nr == '\u034F' {
				visible++
				continue
			}
		}
		if visible%stride == 0 {
			b.WriteString(cgj)
			n++
		}
		visible++
	}
	if n == 0 {
		return seg, 0
	}
	return b.String(), n
}

// obfuscateLatinSecondRune inserts U+034F after the second visible letter or
// digit of each Latin-or-digit subword, unless a CGJ is already there. The
// first-rune mark from L0 is left intact. Idempotent.
func obfuscateLatinSecondRune(text string) (string, int) {
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
			obf, did := markLatinSecond(text[subStart:i])
			b.WriteString(obf)
			if did {
				n++
			}
			subStart = -1
		}
		b.WriteRune(r)
	}
	if subStart >= 0 {
		obf, did := markLatinSecond(text[subStart:])
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

func markLatinSecond(subword string) (string, bool) {
	visible := 0
	insertAt := -1
	for i, r := range subword {
		if r == '\u034F' || r == '\u200B' {
			continue
		}
		visible++
		if visible == 2 {
			size := utf8.RuneLen(r)
			if size <= 0 {
				size = 1
			}
			next := i + size
			if next < len(subword) {
				nr, _ := utf8.DecodeRuneInString(subword[next:])
				if nr == '\u034F' {
					return subword, false
				}
			}
			insertAt = next
			break
		}
	}
	if insertAt < 0 {
		return subword, false
	}
	return subword[:insertAt] + cgj + subword[insertAt:], true
}

// strengthenSystemObfuscation upgrades systemInstruction text from its current
// marks up to targetLevel (1 or 2). Level 1 adds CJK segment marks; level 2
// also marks every CJK letter and the second rune of latin subwords.
// Contents and tools are never touched. n is the number of new CGJs.
func strengthenSystemObfuscation(payload []byte, targetLevel int) ([]byte, int) {
	if len(payload) == 0 || targetLevel < 1 {
		return payload, 0
	}
	total := 0
	any := false
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
			updated, n := applyAdaptiveText(orig, targetLevel)
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
	if !any {
		return payload, total
	}
	return payload, total
}

func applyAdaptiveText(text string, targetLevel int) (string, int) {
	n := 0
	if targetLevel >= 1 {
		stride := adaptiveCJKStrideL1
		if targetLevel >= 2 {
			stride = 1
		}
		var k int
		text, k = obfuscateCJKRuns(text, stride)
		n += k
	}
	if targetLevel >= 2 {
		var k int
		text, k = obfuscateLatinSecondRune(text)
		n += k
	}
	return text, n
}

func adaptiveLevelName(level int) string {
	switch level {
	case 1:
		return "adaptive-L1"
	case 2:
		return "adaptive-L2"
	default:
		return "adaptive-L0"
	}
}
