package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCapacityRetryV8Layout(t *testing.T) {
	src := "routing:\n  retry:\n    request-retry: 1\n    capacity-retry:\n      enabled: true\n      max-attempts: 4\n      base-delay: 500ms\n      max-delay: 6s\n"
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
	cr := cfg.CapacityRetry
	if cr.Enabled == nil || !*cr.Enabled || cr.MaxAttempts != 4 || cr.BaseDelay != "500ms" || cr.MaxDelay != "6s" {
		t.Fatalf("parsed %+v", cr)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "max-attempts: 4") {
		t.Fatalf("capacity-retry missing from marshalled config:\n%s", out)
	}
}
