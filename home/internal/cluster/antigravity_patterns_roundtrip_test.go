package cluster

import (
	"strings"
	"testing"

	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

// Home stores and forwards the proxy runtime config; proxy-only Antigravity
// keys (sensitive-patterns-replace, tos-pause-window) must survive validation
// and the runtime payload round trip untouched.
func TestRuntimeConfigKeepsAntigravitySensitivePatternsAndTOSPause(t *testing.T) {
	yamlDoc := []byte(`config-version: 8
oauth:
  providers:
    antigravity:
      sensitive-patterns-replace:
        - pattern: '(?i)You are Codex,\s+(?:an?\s+)?(?:coding\s+)?agent\s+based\s+on\s+[^.\r\n"]+\.?'
          replacement: "You are an AI coding assistant."
      tos-pause-window: "off"
`)
	if err := appconfig.ValidateV8Config(yamlDoc); err != nil {
		t.Fatalf("validate: %v", err)
	}
	root := map[string]any{
		"config-version": 8,
		"oauth": map[string]any{"providers": map[string]any{"antigravity": map[string]any{
			"sensitive-patterns-replace": []any{map[string]any{"pattern": `(?i)You are Codex,\s+agent`, "replacement": "You are an AI coding assistant."}},
			"tos-pause-window":           "off",
		}}},
	}
	_, payload, err := RuntimeConfigFromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sensitive-patterns-replace", "You are an AI coding assistant.", "tos-pause-window"} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("payload lost %q:\n%s", want, payload)
		}
	}
}
