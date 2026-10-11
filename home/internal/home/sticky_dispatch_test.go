package home

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

type stickyTestAdapter struct {
	modelChannelDispatchTestAdapter
	mu       sync.Mutex
	bindings map[string]string
	outcomes []string
	// raceWinner, when set, is bound by "another Home" between lookup and bind.
	raceWinner string
}

func (a *stickyTestAdapter) LookupAffinity(_ context.Context, k string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.bindings[k]
	return v, ok
}

// BindAffinityFrom mirrors the Redis script: create if absent, refresh if
// unchanged, replace only from the observed value, else keep the winner.
func (a *stickyTestAdapter) BindAffinityFrom(_ context.Context, k, authID, prev, _, _ string, _ time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.raceWinner != "" {
		if _, ok := a.bindings[k]; !ok {
			a.bindings[k] = a.raceWinner
		}
	}
	cur, ok := a.bindings[k]
	if !ok || cur == authID || (prev != "" && cur == prev) {
		a.bindings[k] = authID
		return authID, nil
	}
	return cur, nil
}

func (a *stickyTestAdapter) RecordRequestAffinity(_, _, outcome, _, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.outcomes = append(a.outcomes, outcome)
}

func TestStickyDispatchPinsRespectsGroupsAndRepins(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	modelID := "gemini-sticky-test"
	ids := []string{"auth-s1", "auth-s2", "auth-s3"}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: modelID, Object: "model", Type: "antigravity"}})
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "antigravity", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	adapter := &stickyTestAdapter{bindings: map[string]string{}}
	adapter.allowedModelIDs = []string{modelID}
	rt := &Runtime{coreManager: manager, clusterAdapter: adapter, cfg: &config.Config{}}
	h := http.Header{}
	h.Set(affinityKeyHeader, "conv-1")
	h.Set(affinitySourceHeader, "prefix")

	first, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // round-robin would rotate; sticky must not
		res, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
		if err != nil || res.AuthID != first.AuthID {
			t.Fatalf("request %d: got %v %v, want sticky %s", i, res, err, first.AuthID)
		}
	}
	// Bound credential leaves the key's credential groups -> re-pin elsewhere.
	for _, id := range ids {
		if id != first.AuthID {
			adapter.allowedAuthIDs = append(adapter.allowedAuthIDs, id)
		}
	}
	res, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
	if err != nil || res.AuthID == first.AuthID {
		t.Fatalf("repin: got %v %v", res, err)
	}
	again, _ := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
	if again.AuthID != res.AuthID {
		t.Fatalf("new binding not sticky: %s vs %s", again.AuthID, res.AuthID)
	}
	want := []string{"new", "hit", "hit", "hit", "hit", "hit", "repin", "hit"}
	if len(adapter.outcomes) != len(want) {
		t.Fatalf("outcomes = %v", adapter.outcomes)
	}
	for i := range want {
		if adapter.outcomes[i] != want[i] {
			t.Fatalf("outcomes = %v, want %v", adapter.outcomes, want)
		}
	}
	// Different API key -> independent binding namespace; disabled -> no pinning.
	off := false
	rt.cfg = &config.Config{StickySessions: config.StickySessionsConfig{Enabled: &off}}
	n := len(adapter.outcomes)
	if _, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.outcomes) != n {
		t.Fatalf("disabled sticky still recorded outcomes")
	}
}

// Two Homes serving the first requests of one conversation at the same time:
// the loser keeps its admitted credential for that one request, records
// "race", and the binding stays with the winner so later requests converge.
func TestStickyDispatchFirstBindWinsAcrossHomes(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	modelID := "gemini-sticky-race"
	ids := []string{"auth-r1", "auth-r2"}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: modelID, Object: "model", Type: "antigravity"}})
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "antigravity", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	adapter := &stickyTestAdapter{bindings: map[string]string{}}
	adapter.allowedModelIDs = []string{modelID}
	rt := &Runtime{coreManager: manager, clusterAdapter: adapter, cfg: &config.Config{}}
	h := http.Header{}
	h.Set(affinityKeyHeader, "conv-race")

	// Find which credential round-robin hands out first, then make the other
	// one win the race.
	probe, err := rt.DispatchForAPIKey(context.Background(), modelID, http.Header{}, "client-key")
	if err != nil {
		t.Fatal(err)
	}
	next := ids[0]
	if probe.AuthID == ids[0] {
		next = ids[1]
	}
	winner := ids[0]
	if next == ids[0] {
		winner = ids[1]
	}
	adapter.raceWinner = winner
	res, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthID == winner {
		t.Skip("round-robin picked the winner; race not exercised")
	}
	key := affinityBindingKey("client-key", "conv-race")
	if adapter.bindings[key] != winner {
		t.Fatalf("binding = %s, want winner %s", adapter.bindings[key], winner)
	}
	if got := adapter.outcomes[len(adapter.outcomes)-1]; got != "race" {
		t.Fatalf("outcome = %s, want race", got)
	}
	adapter.raceWinner = ""
	for i := 0; i < 3; i++ {
		again, err := rt.DispatchForAPIKey(context.Background(), modelID, h, "client-key")
		if err != nil || again.AuthID != winner {
			t.Fatalf("follow-up %d: %v %v, want winner %s", i, again, err, winner)
		}
	}
}
