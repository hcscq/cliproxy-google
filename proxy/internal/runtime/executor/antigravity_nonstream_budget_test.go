package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityNonStreamLimitWithBudget(t *testing.T) {
	defer antigravityHealthLive.Store(nil)
	antigravityHealthLive.Store(nil)
	defer cliproxyauth.SetNonStreamRequestDeadline(0)
	cliproxyauth.SetNonStreamRequestDeadline(0)
	s := antigravityHealthSettingsFrom(&config.Config{})
	m := &cliproxyauth.Manager{}
	m.SetMaxRetryDuration(0)
	m.SetMaxRequestCredentials(0)

	// Attempt 1: per-family 150s (not the 180s base, not final).
	ctx := m.WithNonStreamRetryBudget(context.Background())
	if got := s.nonStreamLimit(ctx, "gemini-3.8-flash-tiered"); got != 150*time.Second {
		t.Fatalf("attempt 1 limit = %s, want 150s", got)
	}
	// Attempt 2 after a 150s timeout: final, bounded by the ~90s left of 240s.
	cliproxyauth.AgeRetryBudgetForTest(ctx, 150*time.Second)
	got := s.nonStreamLimit(ctx, "gemini-3.8-flash-tiered")
	if got > 90*time.Second || got < 85*time.Second {
		t.Fatalf("final attempt limit = %s, want ~90s", got)
	}

	// Streaming unchanged: 3.8 family TTFB under a stream budget.
	sctx := m.WithRetryBudget(context.Background())
	if got := s.streamTTFB(sctx, "gemini-3.8-flash-tiered", 0); got != 15*time.Second {
		t.Fatalf("stream TTFB = %s, want 15s default", got)
	}
}
