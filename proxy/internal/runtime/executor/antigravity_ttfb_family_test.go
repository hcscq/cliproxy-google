package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityStreamTTFBByFamily(t *testing.T) {
	defer antigravityHealthLive.Store(nil)
	antigravityHealthLive.Store(nil)
	s := antigravityHealthSettingsFrom(&config.Config{})
	ctx := context.Background()
	if got := s.streamTTFB(ctx, "gemini-3.8-flash-tiered"); got != 28*time.Second {
		t.Fatalf("3.8 default = %s", got)
	}
	if got := s.streamTTFB(ctx, "gemini-2.5-flash"); got != 75*time.Second {
		t.Fatalf("2.5 default = %s", got)
	}
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBTimeoutByFamily: "3.8=20, gemini-2.5=40", TTFBTimeoutSeconds: 60})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.streamTTFB(ctx, "gemini-3.8-flash") != 20*time.Second || s.streamTTFB(ctx, "gemini-2.5-flash") != 40*time.Second || s.streamTTFB(ctx, "gemini-3.1-flash-lite") != 60*time.Second {
		t.Fatalf("live override not applied: %v", s.byFamily)
	}
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBTimeoutByFamily: "off"})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.streamTTFB(ctx, "gemini-3.8-flash") != 75*time.Second {
		t.Fatal("off must disable per-family limits")
	}
	// A longer family value never extends the base limit.
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{TTFBTimeoutByFamily: "3.8=120"})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.streamTTFB(ctx, "gemini-3.8-flash") != 75*time.Second {
		t.Fatal("family limit above base must keep base")
	}
}
