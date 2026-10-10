package home

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	"github.com/tidwall/gjson"
)

func TestDispatchModelInfoWebSearchCapability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		present bool
		enabled bool
	}{
		{
			name:    "enabled",
			raw:     `{"id":"search-alias","native_capabilities":{"web_search":true}}`,
			present: true,
			enabled: true,
		},
		{
			name:    "disabled",
			raw:     `{"id":"search-alias","native_capabilities":{"web_search":false}}`,
			present: true,
			enabled: false,
		},
		{
			name:    "unspecified",
			raw:     `{"id":"search-alias"}`,
			present: false,
			enabled: false,
		},
		{
			name:    "empty_capabilities",
			raw:     `{"id":"search-alias","native_capabilities":{}}`,
			present: false,
			enabled: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			var model registry.ModelInfo
			if err := json.Unmarshal([]byte(tc.raw), &model); err != nil {
				t.Fatal(err)
			}
			authID := "home-search-capability-" + tc.name
			modelRegistry.RegisterClient(authID, "antigravity", []*registry.ModelInfo{&model})
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			result := dispatchModelInfoForAuth(authID, "upstream(high)", "search-alias")
			if result == nil || result.ID != "upstream" {
				t.Fatalf("missing selected route: %+v", result)
			}
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			capability := gjson.GetBytes(payload, "native_capabilities.web_search")
			if capability.Exists() != tc.present || (tc.present && capability.Bool() != tc.enabled) {
				t.Fatalf("capability mismatch: want present=%v enabled=%v, got: %s", tc.present, tc.enabled, payload)
			}
		})
	}
}
