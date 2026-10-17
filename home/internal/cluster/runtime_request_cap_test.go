package cluster

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

// Request caps are evaluated from the light runtime auth; its metadata must
// carry the tier and the per-credential override.
func TestModelMetadataKeepsRequestCapKeys(t *testing.T) {
	a := &coreauth.Auth{ID: "x", Metadata: map[string]any{
		"access_token": "secret", "subscription_tier": "g1-pro-tier",
		"request_cap_mode": "custom", "request_cap_hourly": int64(3), "request_cap_daily": int64(0),
	}}
	m := modelMetadataFromAuth(a)
	if m["request_cap_mode"] != "custom" || m["request_cap_hourly"] != int64(3) || m["subscription_tier"] != "g1-pro-tier" {
		t.Fatalf("missing cap keys: %v", m)
	}
	if _, leaked := m["access_token"]; leaked {
		t.Fatal("token must not be kept")
	}
}
