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
	if got := s.streamTTFB(ctx, "gemini-3.8-flash-tiered"); got != 15*time.Second {
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

func TestAntigravityNonStreamLimitByFamily(t *testing.T) {
	defer antigravityHealthLive.Store(nil)
	antigravityHealthLive.Store(nil)
	ctx := context.Background()
	s := antigravityHealthSettingsFrom(&config.Config{})
	if got := s.nonStreamLimit(ctx, "gemini-3.8-flash-tiered"); got != 120*time.Second {
		t.Fatalf("3.8 non-stream default = %s", got)
	}
	if got := s.nonStreamLimit(ctx, "gemini-2.5-flash"); got != 180*time.Second {
		t.Fatalf("2.5 non-stream default = %s", got)
	}
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{NonStreamTimeoutByFamily: "3.8=90", TTFBTimeoutByFamily: "3.8=20"})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.nonStreamLimit(ctx, "gemini-3.8-flash") != 90*time.Second || s.streamTTFB(ctx, "gemini-3.8-flash") != 20*time.Second {
		t.Fatalf("live override not applied: %v %v", s.nonStreamByFamily, s.byFamily)
	}
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{NonStreamTimeoutByFamily: "3.8=300"})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.nonStreamLimit(ctx, "gemini-3.8-flash") != 180*time.Second {
		t.Fatal("family limit above base must keep base")
	}
	SetAntigravityHealthConfig(config.AntigravityHealthConfig{NonStreamTimeoutByFamily: "off"})
	s = antigravityHealthSettingsFrom(&config.Config{})
	if s.nonStreamLimit(ctx, "gemini-3.8-flash") != 180*time.Second {
		t.Fatal("off must disable")
	}
}
