package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEmpty502CooldownV8Layout(t *testing.T) {
	src := "routing:\n  cooldown:\n    empty-502-streak: 2\n    empty-502-backoff-seconds: 120\n"
	if err := ValidateV8Config([]byte(src)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Empty502StreakThreshold() != 2 || cfg.Empty502Backoff() != 120*time.Second {
		t.Fatalf("threshold=%d backoff=%s", cfg.Empty502StreakThreshold(), cfg.Empty502Backoff())
	}

	def := Config{}
	if def.Empty502StreakThreshold() != 3 || def.Empty502Backoff() != 90*time.Second {
		t.Fatalf("defaults threshold=%d backoff=%s", def.Empty502StreakThreshold(), def.Empty502Backoff())
	}
	off := Config{Empty502Streak: -1, Empty502BackoffSeconds: 1}
	if off.Empty502StreakThreshold() != 0 || off.Empty502Backoff() != 5*time.Second {
		t.Fatalf("disabled/clamped threshold=%d backoff=%s", off.Empty502StreakThreshold(), off.Empty502Backoff())
	}
	huge := Config{Empty502Streak: 1000, Empty502BackoffSeconds: 99999}
	if huge.Empty502StreakThreshold() != 100 || huge.Empty502Backoff() != time.Hour {
		t.Fatalf("capped threshold=%d backoff=%s", huge.Empty502StreakThreshold(), huge.Empty502Backoff())
	}
}
