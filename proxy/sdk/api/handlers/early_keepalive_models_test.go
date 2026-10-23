package handlers

import "testing"

func TestEarlyKeepAliveModelMatch(t *testing.T) {
	if !earlyKeepAliveModelMatch("gemini-4", "models/Gemini-4-argon-high") {
		t.Fatal("should match")
	}
	if earlyKeepAliveModelMatch("gemini-4", "gemini-3.8-flash") || earlyKeepAliveModelMatch("", "gemini-4-argon") {
		t.Fatal("should not match")
	}
}
