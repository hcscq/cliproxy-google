package auth

import "testing"

func TestStallExemptFamilies(t *testing.T) {
	s := DefaultHealthSettings()
	if !s.StallExemptModel("gemini-4-argon-high") || s.StallExemptModel("gemini-3.8-flash-high") {
		t.Fatal("default exemption wrong")
	}
	if len(ParseStallExemptFamilies("off")) != 0 {
		t.Fatal("off")
	}
	if !ParseStallExemptFamilies("3.8")["gemini-3.8"] {
		t.Fatal("3.8")
	}
}
