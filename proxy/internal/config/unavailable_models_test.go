package config

import "testing"

func TestUnavailableModelsPanelPath(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("oauth:\n  providers:\n    antigravity:\n      health:\n        unavailable-models: \"gemini-2.5-pro, foo\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Antigravity.Health.UnavailableModels == nil || *cfg.Antigravity.Health.UnavailableModels != "gemini-2.5-pro, foo" {
		t.Fatalf("got %v", cfg.Antigravity.Health.UnavailableModels)
	}
	cfg, err = ParseConfigBytes([]byte("oauth:\n  providers:\n    antigravity:\n      health:\n        retry-empty-response: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Antigravity.Health.UnavailableModels != nil {
		t.Fatal("unset should stay nil (default list)")
	}
}
