package management

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func tierAuth(id, tier string) *coreauth.Auth {
	a := &coreauth.Auth{ID: id, Provider: "antigravity", Metadata: map[string]any{}}
	if tier != "" {
		a.Metadata["subscription_tier"] = tier
	}
	return a
}

func TestCredentialTierBucket(t *testing.T) {
	for tier, want := range map[string]string{
		"g1-ultra-tier": "ultra", "g1-pro-tier": "pro", "free-tier": "free", "standard-tier": "other", "": "unknown",
	} {
		if got := credentialTierBucket(tierAuth("x", tier)); got != want {
			t.Errorf("%q -> %q, want %q", tier, got, want)
		}
	}
	if got := credentialTierBucket(&coreauth.Auth{ID: "nil-meta"}); got != "unknown" {
		t.Errorf("nil metadata -> %q", got)
	}
}

func TestCredFilterTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/credentials/search?tier=Pro,unknown", nil)
	f := credFilterFromQuery(c)
	cases := map[string]bool{"g1-pro-tier": true, "": true, "free-tier": false, "g1-ultra-tier": false}
	for tier, want := range cases {
		if got := f.match(tierAuth("a-"+tier, tier), "ok", nil, nil); got != want {
			t.Errorf("tier=Pro,unknown match(%q) = %v, want %v", tier, got, want)
		}
	}
	c2, _ := gin.CreateTestContext(httptest.NewRecorder()) // gin caches the parsed query per context
	c2.Request = httptest.NewRequest("GET", "/credentials/search", nil)
	if !credFilterFromQuery(c2).match(tierAuth("b", "free-tier"), "ok", nil, nil) {
		t.Error("no tier filter must match everything")
	}
}
