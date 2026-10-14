package cluster

import (
	"strings"
	"testing"
)

func TestRuntimeConfigKeepsAntigravityHealth(t *testing.T) {
	root := map[string]any{
		"config-version": 8,
		"oauth": map[string]any{"providers": map[string]any{"antigravity": map[string]any{"health": map[string]any{
			"ttfb-timeout-by-family": "3.8=28",
			"cluster":                map[string]any{"demote-fail-percent": 40},
		}}}},
	}
	cfg, payload, err := RuntimeConfigFromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Antigravity.Health.TTFBTimeoutByFamily != "3.8=28" || cfg.Antigravity.Health.Cluster.DemoteFailPercent != 40 {
		t.Fatalf("health not decoded: %+v", cfg.Antigravity.Health)
	}
	if !strings.Contains(string(payload), "ttfb-timeout-by-family") {
		t.Fatalf("payload lost health:\n%s", payload)
	}
}
