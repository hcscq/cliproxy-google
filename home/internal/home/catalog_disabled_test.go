package home

import "testing"

func TestParseAntigravityCatalog_SkipsDisabled(t *testing.T) {
	got, err := parseAntigravityCatalog([]byte(`{"models":{"gemini-4-argon-disabled":{"displayName":"Gemini 4 Argon","disabled":true},"gemini-3-flash":{"displayName":"Gemini 3 Flash"},"gemini-4-argon-high":{"displayName":"Gemini 4 Argon (High)","disabled":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["gemini-4-argon-disabled"]; ok || len(got) != 2 {
		t.Fatalf("disabled entry must be skipped, got %v", got)
	}
}
