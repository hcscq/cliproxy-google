package home

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

const tosBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal.","status":"PERMISSION_DENIED"}}`

func TestReserveAIProbeDefaultOff(t *testing.T) {
	var c *config.Config
	if c.ReserveSettings().AIProbe {
		t.Fatal("nil config: AI probe must default off")
	}
	c = &config.Config{}
	if c.ReserveSettings().AIProbe {
		t.Fatal("unset: AI probe must default off")
	}
	on := true
	c.ReserveAIProbe = &on
	if !c.ReserveSettings().AIProbe {
		t.Fatal("explicit on ignored")
	}
}

func TestIsTOSBanOnlyTermsNotVerify(t *testing.T) {
	if _, _, ok := isTOSBan(http.StatusForbidden, tosBody); !ok {
		t.Fatal("TOS 403 must be a list ban")
	}
	if _, _, ok := isTOSBan(http.StatusForbidden, verifyBody); ok {
		t.Fatal("verify-your-account must not be a list ban")
	}
	if _, _, ok := isTOSBan(http.StatusTooManyRequests, tosBody); ok {
		t.Fatal("non-403 must not be a ban")
	}
}

func TestFilterReservedPairs(t *testing.T) {
	pairs := []quotaProbeTarget{{auth: &coreauth.Auth{ID: "r1"}, model: "m"}, {auth: &coreauth.Auth{ID: "a1"}, model: "m"}}
	out := filterReservedPairs(pairs, map[string]time.Time{"r1": time.Now()})
	if len(out) != 1 || out[0].auth.ID != "a1" {
		t.Fatalf("reserved pair not dropped: %+v", out)
	}
	if got := filterReservedPairs(pairs, nil); len(got) != 2 {
		t.Fatal("no reserves: keep all")
	}
}

func TestClassifyHealthGenSkippedIsNotFailure(t *testing.T) {
	p := HealthProbeResult{GenSkipped: true, TierStatus: http.StatusOK, Tier: "g1-pro-tier"}
	in := HealthInputs{QuotaWindows: 2, QuotaMin: 1, CatalogOK: true, CatalogCount: 10}
	v := ClassifyHealth(p, in, config.ImportCheckSettings{ExpectedTier: "pro", QuotaMin: 0.9})
	if v.Status != "pass" {
		t.Fatalf("skipped test request must not fail the check: %+v", v)
	}
}

type fullAuthCatalogAdapter struct {
	fakeCatalogAdapter
	m *coreauth.Manager
}

func (f *fullAuthCatalogAdapter) GetFullAuth(_ context.Context, id string) (*coreauth.Auth, error) {
	a, _ := f.m.GetByID(id)
	return a, nil
}

func TestCatalogTOS403DisablesCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(tosBody))
	}))
	old := antigravityModelsURL
	antigravityModelsURL = srv.URL
	t.Cleanup(func() { antigravityModelsURL = old; srv.Close() })
	ad := &fullAuthCatalogAdapter{}
	rt := newCatalogTestRuntime(t, "tos-1", ad)
	ad.m = rt.coreManager
	if _, err := rt.refreshAntigravityCatalogNow(context.Background(), "tos-1", "test"); err == nil {
		t.Fatal("expected fetch error")
	}
	got, _ := rt.coreManager.GetByID("tos-1")
	if !got.Disabled || got.Metadata[AccountBlockReasonKey] != accountBlockSuspended {
		t.Fatalf("TOS 403 on model list did not disable: %+v", got)
	}
}

func TestCatalogVerify403DoesNotDisable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(verifyBody))
	}))
	old := antigravityModelsURL
	antigravityModelsURL = srv.URL
	t.Cleanup(func() { antigravityModelsURL = old; srv.Close() })
	ad := &fullAuthCatalogAdapter{}
	rt := newCatalogTestRuntime(t, "ver-1", ad)
	ad.m = rt.coreManager
	_, _ = rt.refreshAntigravityCatalogNow(context.Background(), "ver-1", "test")
	got, _ := rt.coreManager.GetByID("ver-1")
	if got.Disabled {
		t.Fatal("verify 403 on model list must keep existing handling (no list-based disable)")
	}
}
