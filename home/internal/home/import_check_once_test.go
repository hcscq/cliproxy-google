package home

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

func reserveProbeRuntime(t *testing.T, id string) *Runtime {
	t.Helper()
	m := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive,
		ProxyURL: "http://127.0.0.1:1", Metadata: map[string]any{"access_token": "tok"}}
	if _, err := m.Register(coreauth.WithSkipPersist(context.Background()), a); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{coreManager: m}
	r.reserveStoreOverride = &fakeReserveStore{reserved: map[string]time.Time{id: time.Now()}}
	r.reserveCfgOverride = &config.Config{} // ai-probe default off
	return r
}

func stubHealthSend(t *testing.T, codes ...int) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old, oldDelay := healthSend, healthImportRetryDelay
	healthImportRetryDelay = time.Millisecond
	healthSend = func(context.Context, *coreauth.Auth, string) (int, http.Header, []byte, error) {
		i := int(n.Add(1)) - 1
		c := codes[len(codes)-1]
		if i < len(codes) {
			c = codes[i]
		}
		if c == 0 {
			return 0, nil, nil, errors.New("dial: connection reset")
		}
		body := []byte(`{}`)
		if c == http.StatusForbidden {
			body = []byte(verifyBody)
		}
		return c, nil, body, nil
	}
	t.Cleanup(func() { healthSend, healthImportRetryDelay = old, oldDelay })
	return &n
}

func TestImportCheckSendsOneTestRequestForReserve(t *testing.T) {
	r := reserveProbeRuntime(t, "imp-r1")
	n := stubHealthSend(t, http.StatusForbidden)
	p := r.HealthProbeWith(context.Background(), "imp-r1", "gemini-2.5-flash", true)
	if n.Load() != 1 || p.GenSkipped || p.GenStatus != http.StatusForbidden {
		t.Fatalf("import check must send exactly one request: calls=%d %+v", n.Load(), p)
	}
	v := ClassifyHealth(p, HealthInputs{QuotaWindows: 1, QuotaMin: 1, CatalogOK: true, CatalogCount: 5}, config.ImportCheckSettings{QuotaMin: 0.9})
	found := false
	for _, c := range v.Categories {
		found = found || c == HealthNeedsVerify
	}
	if v.Status != "fail" || !found {
		t.Fatalf("VALIDATION_REQUIRED at import must fail as needs-verify: %+v", v)
	}
}

func TestRecheckOfReserveSendsNoTestRequest(t *testing.T) {
	r := reserveProbeRuntime(t, "imp-r2")
	n := stubHealthSend(t, http.StatusOK)
	p := r.HealthProbeWith(context.Background(), "imp-r2", "gemini-2.5-flash", false)
	if n.Load() != 0 || !p.GenSkipped {
		t.Fatalf("recheck of a reserve must not send: calls=%d %+v", n.Load(), p)
	}
}

func TestImportCheckRetriesOnceOnTransient(t *testing.T) {
	r := reserveProbeRuntime(t, "imp-r3")
	n := stubHealthSend(t, 0, 0, 0)
	p := r.HealthProbeWith(context.Background(), "imp-r3", "gemini-2.5-flash", true)
	if n.Load() != 2 || p.GenError == "" {
		t.Fatalf("want one retry (2 calls) then error: calls=%d %+v", n.Load(), p)
	}
	n2 := stubHealthSend(t, http.StatusForbidden)
	_ = r.HealthProbeWith(context.Background(), "imp-r3", "gemini-2.5-flash", true)
	if n2.Load() != 1 {
		t.Fatalf("403 must not be retried: calls=%d", n2.Load())
	}
}
