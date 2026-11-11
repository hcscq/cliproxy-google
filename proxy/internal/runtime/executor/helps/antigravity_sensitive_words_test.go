package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const cgjTest = "\u034F"

func TestAntigravityMatcherUsesU034FPerSubword(t *testing.T) {
	m := BuildAntigravitySensitiveWordMatcher([]string{"Nous Research"}, nil)
	if m == nil {
		t.Fatal("matcher nil")
	}
	got := m.ObfuscateText("created by Nous Research.")
	want := "created by N" + cgjTest + "ous R" + cgjTest + "esearch."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if strings.Contains(got, "\u200B") {
		t.Fatal("must not use U+200B on the Antigravity path")
	}
}

func TestAntigravityMatcherDevinPathStillU200B(t *testing.T) {
	m := BuildSensitiveWordMatcher([]string{"proxy"})
	got := m.ObfuscateText("use proxy")
	if got != "use p\u200Broxy" {
		t.Fatalf("Devin path changed: %q", got)
	}
}

func TestAntigravityLiteralReplacementSinglePassLongestFirst(t *testing.T) {
	// longest match first: "Claude Code" wins over "Claude"; single pass (no
	// cascade): the replacement text is not re-scanned.
	repl := map[string]string{"Claude Code": "the assistant", "Claude": "Clause"}
	m := BuildAntigravitySensitiveWordMatcher(nil, repl)
	got := m.ObfuscateText("I am Claude Code here")
	if got != "I am the assistant here" {
		t.Fatalf("got %q", got)
	}
	// no cascade: a replacement that outputs another target is left alone
	m2 := BuildAntigravitySensitiveWordMatcher(nil, map[string]string{"foo": "barbar", "bar": "X"})
	if out := m2.ObfuscateText("foo"); out != "barbar" {
		t.Fatalf("cascade happened: %q", out)
	}
}

func TestAntigravityReplaceAndObfuscateCoexist(t *testing.T) {
	m := BuildAntigravitySensitiveWordMatcher([]string{"Hermes"}, map[string]string{"GPT-5": "the model"})
	got := m.ObfuscateText("Hermes uses GPT-5")
	if !strings.Contains(got, "H"+cgjTest+"ermes") || !strings.Contains(got, "the model") {
		t.Fatalf("coexist failed: %q", got)
	}
}

func TestObfuscateSystemInstructionOnlyNotMessages(t *testing.T) {
	m := BuildAntigravitySensitiveWordMatcher([]string{"proxy"}, nil)
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Use proxy"}]},"contents":[{"role":"user","parts":[{"text":"proxy here"}]}]}}`)
	out := ObfuscateSensitiveWordsInSystemInstruction(payload, m)
	if gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String() != "Use p"+cgjTest+"roxy" {
		t.Fatal("system instruction not obfuscated")
	}
	if gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != "proxy here" {
		t.Fatal("contents must be untouched")
	}
}

func TestAlreadyObfuscatedNotDoubleProcessed(t *testing.T) {
	m := BuildAntigravitySensitiveWordMatcher([]string{"Global"}, nil)
	// text already carries a CGJ inside the target -> literal no longer matches
	in := "G" + cgjTest + "lobal rules"
	if got := m.ObfuscateText(in); got != in {
		t.Fatalf("double processed: %q", got)
	}
}

func TestStripCloakCharsBothMarkers(t *testing.T) {
	in := "He\u200Bllo W\u034Forld"
	if got := StripCloakChars(in); got != "Hello World" {
		t.Fatalf("got %q", got)
	}
	if StripCloakChars("clean") != "clean" {
		t.Fatal("no-op expected")
	}
}

func TestEmptyConfigMatcherNil(t *testing.T) {
	if BuildAntigravitySensitiveWordMatcher(nil, nil) != nil {
		t.Fatal("empty config must yield nil matcher (no-op, default off)")
	}
}
