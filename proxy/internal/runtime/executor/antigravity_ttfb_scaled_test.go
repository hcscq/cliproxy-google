package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityTTFBInputScaled(t *testing.T) {
	defer antigravityHealthLive.Store(nil)
	ctx := context.Background()
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{})
	s := antigravityHealthSettingsFrom(nil)
	m := "gemini-3.8-flash-low"
	cases := map[int]time.Duration{0: 15 * time.Second, 4000: 15*time.Second + 500*time.Millisecond, 80000: 25 * time.Second, 112000: 29 * time.Second, 10_000_000: 45 * time.Second}
	for n, want := range cases {
		if got := s.streamTTFB(ctx, m, n); got != want {
			t.Fatalf("bytes=%d got %s want %s", n, got, want)
		}
	}
	// Families without an override keep the base limit regardless of size.
	if got := s.streamTTFB(ctx, "gemini-2.5-flash", 1_000_000); got != 75*time.Second {
		t.Fatalf("2.5 %s", got)
	}
	zero := 0
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBExtraSecondsPer10kTokens: &zero})
	if got := antigravityHealthSettingsFrom(nil).streamTTFB(ctx, m, 1_000_000); got != 15*time.Second {
		t.Fatalf("off %s", got)
	}
	ten := 10
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBExtraSecondsPer10kTokens: &ten, TTFBScaledMaxSeconds: 30, TTFBTimeoutSeconds: 25})
	if got := antigravityHealthSettingsFrom(nil).streamTTFB(ctx, m, 1_000_000); got != 25*time.Second {
		t.Fatalf("base cap %s", got)
	}
}
