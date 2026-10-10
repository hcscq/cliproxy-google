package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapacityRetryV8Layout(t *testing.T) {
	src := "routing:\n  retry:\n    capacity-retry:\n      enabled: false\n      max-attempts: 2\n      base-delay: 2s\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	cr := cfg.CapacityRetry
	if cr.Enabled == nil || *cr.Enabled || cr.MaxAttempts != 2 || cr.BaseDelay != "2s" {
		t.Fatalf("parsed %+v", cr)
	}
}
