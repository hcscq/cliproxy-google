package cliproxy

import "testing"

func TestParseAntigravityHints_SkipsDisabled(t *testing.T) {
	h, ok := parseAntigravityModelCapabilityHints([]byte(`{"models":{"gemini-4-argon-disabled":{"disabled":true},"gemini-3-flash":{}}}`))
	if !ok {
		t.Fatal("parse failed")
	}
	if _, bad := h.ModelIDs["gemini-4-argon-disabled"]; bad || len(h.ModelIDs) != 1 {
		t.Fatalf("got %v", h.ModelIDs)
	}
}
