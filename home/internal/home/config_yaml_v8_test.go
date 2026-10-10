package home

import (
	"strings"
	"testing"

	cpaconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV8DownstreamConfigFiltersSecretsAndKeepsScope(t *testing.T) {
	source := `config-version: 8
server:
  port: 8427
  tls: {enable: true, cert: secret-cert, key: secret-private-key}
management: {secret-key: management-secret}
access: {api-keys: [client-secret]}
api-keys:
  codex: [{base-url: 'https://example.test', keys: [{api-key: upstream-secret}]}]
oauth:
  auth-dir: secret-auth-dir
  model-alias: {codex: [{name: hidden-model, alias: hidden-alias}]}
  providers:
    codex: {disable-codex-cloaking: true, header-defaults: {user-agent: oauth-agent}}
    aistudio: {ws-auth: true}
routing:
  cooldown: {disable-cooling: false}
observability:
  usage: {usage-statistics-enabled: false}
`
	data, err := sanitizeConfigYAMLForDownstream([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-", "management-secret", "client-secret", "upstream-secret", "hidden-alias"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("downstream contains filtered value %q", secret)
		}
	}
	cpa, err := cpaconfig.ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if cpa.Port != 8427 || !cpa.DisableCooling || !cpa.UsageStatisticsEnabled || cpa.WebsocketAuth {
		t.Fatal("Home invariants lost")
	}
	if !cpa.Codex.DisableCodexCloaking || !cpa.ForAPIKey().Codex.DisableCodexCloaking || cpa.CodexHeaderDefaults.UserAgent != "oauth-agent" || cpa.ForAPIKey().CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("shared client settings or OAuth-only header scope lost")
	}
	if !strings.Contains(string(data), "disable-codex-cloaking: true") || !strings.Contains(string(data), "port: 8427") {
		t.Fatal("legacy projection missing")
	}
}
