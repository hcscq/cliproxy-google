package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGemini4FamilyLengthensTTFB(t *testing.T) {
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{})
	defer antigravityHealthLive.Store(nil)
	s := antigravityHealthSettingsFrom(nil)
	if got := s.streamTTFB(context.Background(), "gemini-4-argon-high", 400000); got != 240*time.Second {
		t.Fatalf("argon stream ttfb = %s, want 240s", got)
	}
	if got := s.streamTTFB(context.Background(), "gemini-2.5-pro", 0); got != 75*time.Second {
		t.Fatalf("2.5 stream ttfb = %s, want 75s", got)
	}
	if got := s.streamTTFB(context.Background(), "gemini-3.8-flash-high", 0); got != 15*time.Second {
		t.Fatalf("3.8 stream ttfb = %s, want 15s", got)
	}
	if got := s.nonStreamLimit(context.Background(), "gemini-4-argon"); got != 300*time.Second {
		t.Fatalf("argon nonstream = %s, want 300s", got)
	}
	if !s.stallExemptModel("gemini-4-argon-low") || s.stallExemptModel("gemini-3.8-flash-high") {
		t.Fatal("stall exempt defaults wrong")
	}
	if got := antigravityRequestDeadlineFloor("gemini-4-argon-high", true); got != 270*time.Second {
		t.Fatalf("deadline floor = %s, want 270s", got)
	}
	if got := antigravityRequestDeadlineFloor("gemini-2.5-pro", true); got != 0 {
		t.Fatalf("2.5 floor = %s, want 0", got)
	}
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{Cluster: config.AntigravityClusterHealthConfig{StallExemptFamilies: "off"}})
	if antigravityHealthSettingsFrom(nil).stallExemptModel("gemini-4-argon") {
		t.Fatal("off should disable exemption")
	}
}

func TestTTFBFinalErrBody(t *testing.T) {
	e := antigravityTTFBTimeoutFinalErr("gemini-4-argon-high", 240*time.Second)
	if e.code != 504 || e.retryAfter == nil || *e.retryAfter != time.Millisecond {
		t.Fatalf("bad final err %+v", e)
	}
}
