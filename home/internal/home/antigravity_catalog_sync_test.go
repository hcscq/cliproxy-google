package home

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

type fakeCatalogAdapter struct {
	mu   sync.Mutex
	rows map[string]AntigravityCatalogRow
}

func (f *fakeCatalogAdapter) Enabled() bool                                  { return true }
func (f *fakeCatalogAdapter) LoadAuthIndex(context.Context) error            { return nil }
func (f *fakeCatalogAdapter) ListMinimalAuths() []*coreauth.Auth             { return nil }
func (f *fakeCatalogAdapter) LoadConfigYAML(context.Context) ([]byte, error) { return nil, nil }
func (f *fakeCatalogAdapter) GetFullAuth(context.Context, string) (*coreauth.Auth, error) {
	return nil, nil
}
func (f *fakeCatalogAdapter) LoadAntigravityCatalogs(context.Context) (map[string]AntigravityCatalogRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]AntigravityCatalogRow{}
	for k, v := range f.rows {
		out[k] = v
	}
	return out, nil
}
func (f *fakeCatalogAdapter) SaveAntigravityCatalog(_ context.Context, row AntigravityCatalogRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]AntigravityCatalogRow{}
	}
	f.rows[row.AuthID] = row
	return nil
}

func newCatalogTestRuntime(t *testing.T, authID string, adapter ClusterAdapter) *Runtime {
	t.Helper()
	m := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: authID, Index: authID, Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "tok"}}
	if _, err := m.Register(coreauth.WithSkipPersist(context.Background()), a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		antigravityCatalogMu.Lock()
		delete(antigravityCatalog, authID)
		antigravityCatalogMu.Unlock()
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	return &Runtime{coreManager: m, clusterAdapter: adapter}
}

func catalogServer(t *testing.T, models *atomic.Value, calls *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ids := models.Load().([]string)
		var b strings.Builder
		b.WriteString(`{"models":{`)
		for i, id := range ids {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"` + id + `":{"maxTokens":1000}`)
		}
		b.WriteString(`}}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	old := antigravityModelsURL
	antigravityModelsURL = srv.URL
	t.Cleanup(func() { antigravityModelsURL = old; srv.Close() })
}

func TestParseCatalogInterval(t *testing.T) {
	cases := map[string]time.Duration{"": 15 * time.Minute, "off": 0, "0": 0, "30m": 30 * time.Minute, "10s": time.Minute, "bogus": 15 * time.Minute}
	for in, want := range cases {
		if got := parseCatalogInterval(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

func TestCatalogDue(t *testing.T) {
	now := time.Now()
	if !catalogDue(AntigravityCatalogRow{}, false, time.Minute, now) {
		t.Fatal("unknown row must be due")
	}
	fresh := AntigravityCatalogRow{Status: "ok", FetchedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute)}
	if catalogDue(fresh, true, 15*time.Minute, now) {
		t.Fatal("fresh row must not be due")
	}
	if !catalogDue(fresh, true, 30*time.Second, now) {
		t.Fatal("stale row must be due")
	}
	if catalogDue(fresh, true, 0, now) {
		t.Fatal("timer off: not due")
	}
	failed := AntigravityCatalogRow{Status: "error", FetchedAt: now.Add(-time.Hour), AttemptedAt: now.Add(-time.Minute)}
	if catalogDue(failed, true, time.Minute, now) {
		t.Fatal("error backoff must apply")
	}
}

func TestDiffCatalogTracksAddedRemoved(t *testing.T) {
	now := time.Date(2026, 10, 11, 5, 0, 0, 0, time.UTC)
	prev := map[string]antigravityUpstreamModel{"a": {ID: "a"}, "b": {ID: "b"}}
	next := map[string]antigravityUpstreamModel{"a": {ID: "a"}, "c": {ID: "c"}}
	added, removed := diffCatalog(prev, next, "", "", now)
	if !strings.Contains(added, `"c"`) || !strings.Contains(removed, `"b"`) {
		t.Fatalf("added=%s removed=%s", added, removed)
	}
	// b comes back: no longer removed; old entries are pruned after a week.
	back := map[string]antigravityUpstreamModel{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}}
	_, removed2 := diffCatalog(next, back, added, removed, now.Add(time.Hour))
	if removed2 != "" {
		t.Fatalf("returned model must leave removed: %s", removed2)
	}
	added3, _ := diffCatalog(back, back, added, "", now.Add(8*24*time.Hour))
	if added3 != "" {
		t.Fatalf("old additions must be pruned: %s", added3)
	}
	// first fetch (no previous catalog) records nothing
	if a, r := diffCatalog(nil, next, "", "", now); a != "" || r != "" {
		t.Fatalf("first fetch: %s %s", a, r)
	}
}

func TestCatalogLeaderFetchesStoresAndRemovesModel(t *testing.T) {
	var models atomic.Value
	models.Store([]string{"gemini-3.8-flash-tiered", "gemini-3.6-flash-tiered"})
	var calls atomic.Int32
	catalogServer(t, &models, &calls)
	store := &fakeCatalogAdapter{}
	rt := newCatalogTestRuntime(t, "cat-a", store)
	rt.SetLeaderFunc(func() bool { return true })

	rt.antigravityCatalogTick(context.Background(), "startup")
	if calls.Load() != 1 || store.rows["cat-a"].ModelCount != 2 {
		t.Fatalf("calls=%d row=%+v", calls.Load(), store.rows["cat-a"])
	}
	if !registry.GetGlobalRegistry().ClientSupportsModel("cat-a", "gemini-3.6-flash-tiered") {
		t.Fatal("model must be registered after first fetch")
	}
	rt.antigravityCatalogTick(context.Background(), "timer")
	if calls.Load() != 1 {
		t.Fatal("fresh catalog must not be fetched again")
	}

	models.Store([]string{"gemini-3.8-flash-tiered"})
	if _, err := rt.refreshAntigravityCatalogNow(context.Background(), "cat-a", "manual"); err != nil {
		t.Fatal(err)
	}
	if registry.GetGlobalRegistry().ClientSupportsModel("cat-a", "gemini-3.6-flash-tiered") {
		t.Fatal("removed model must be unregistered")
	}
	st := rt.AntigravityCatalogStatuses()
	if len(st) != 1 || len(st[0].Removed) != 1 || st[0].Removed[0].Model != "gemini-3.6-flash-tiered" || st[0].Trigger != "manual" {
		t.Fatalf("status %+v", st)
	}
}

func TestCatalogFollowerOnlyReadsStore(t *testing.T) {
	var models atomic.Value
	models.Store([]string{"x"})
	var calls atomic.Int32
	catalogServer(t, &models, &calls)
	now := time.Now().UTC()
	store := &fakeCatalogAdapter{rows: map[string]AntigravityCatalogRow{"cat-f": {AuthID: "cat-f", Raw: []byte(`{"models":{"gemini-from-leader":{}}}`), FetchedAt: now, AttemptedAt: now, Status: "ok", ModelCount: 1}}}
	rt := newCatalogTestRuntime(t, "cat-f", store)
	rt.SetLeaderFunc(func() bool { return false })
	rt.antigravityCatalogTick(context.Background(), "timer")
	if calls.Load() != 0 {
		t.Fatal("follower must not call upstream on the timer")
	}
	if !registry.GetGlobalRegistry().ClientSupportsModel("cat-f", "gemini-from-leader") {
		t.Fatal("follower must apply the stored catalog")
	}
}

func TestCatalogEventRefreshIsDebounced(t *testing.T) {
	var models atomic.Value
	models.Store([]string{"m"})
	var calls atomic.Int32
	catalogServer(t, &models, &calls)
	rt := newCatalogTestRuntime(t, "cat-e", nil)
	if !rt.TriggerAntigravityCatalogRefresh("cat-e", "404 m") {
		t.Fatal("first event must trigger")
	}
	if rt.TriggerAntigravityCatalogRefresh("cat-e", "404 m") {
		t.Fatal("second event within a minute must be debounced")
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
