package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravity38FirstByteDefault15s(t *testing.T) {
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{})
	defer antigravityHealthLive.Store(nil)
	s := antigravityHealthSettingsFrom(nil)
	for _, m := range []string{"gemini-3.8-flash-tiered", "gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash"} {
		if got := s.streamTTFB(context.Background(), m); got != 15*time.Second {
			t.Fatalf("%s: %s", m, got)
		}
	}
	for _, m := range []string{"gemini-2.5-flash", "gemini-3-flash", "gemini-3.1-pro-preview"} {
		if got := s.streamTTFB(context.Background(), m); got != 75*time.Second {
			t.Fatalf("%s unchanged: %s", m, got)
		}
	}
	// Emulate pass-1 stays capped at half the first-byte deadline.
	if got := antigravityBuiltinPresearchTimeout(s.streamTTFB(context.Background(), "gemini-3.8-flash-tiered")); got != 7500*time.Millisecond {
		t.Fatalf("presearch cap %s", got)
	}
	// Panel override wins.
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBTimeoutByFamily: "3.8=25"})
	if got := antigravityHealthSettingsFrom(nil).streamTTFB(context.Background(), "gemini-3.8-flash-tiered"); got != 25*time.Second {
		t.Fatalf("override %s", got)
	}
}

func TestAntigravityTTFBErrDefaultCooldown(t *testing.T) {
	e := antigravityTTFBTimeoutErr("gemini-3.8-flash-tiered", 15*time.Second, 0)
	if e.retryAfter == nil || *e.retryAfter != 45*time.Second {
		t.Fatalf("retryAfter %v", e.retryAfter)
	}
}
