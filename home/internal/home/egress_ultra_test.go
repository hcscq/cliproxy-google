package home

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestUltraLimitDefault(t *testing.T) {
	var v EgressSettingsValue
	if v.UltraLimit() != 1 {
		t.Fatal("default should be 1")
	}
	_ = json.Unmarshal([]byte(`{"ultra_max_per_egress":0}`), &v)
	if v.UltraLimit() != 0 {
		t.Fatal("explicit 0 = unlimited")
	}
	_ = json.Unmarshal([]byte(`{"ultra_max_per_egress":3}`), &v)
	if v.UltraLimit() != 3 {
		t.Fatal("3")
	}
}

func TestPlanUltraPlacement(t *testing.T) {
	specs := ParseEgressSpecs("a=http://h1:1=1.1.1.1,b=http://h2:1=2.2.2.2,c=http://h3:1=3.3.3.3")
	st := map[string]*EgressStatus{"http://h1:1": {Healthy: true}, "http://h2:1": {Healthy: true}, "http://h3:1": {Healthy: false}}
	uc := map[string]int{"http://h1:1": 1}
	counts := map[string]int{"http://h1:1": 5, "http://h2:1": 9}
	got := planUltraPlacement([]ultraCand{{"n1", "http://h1:1"}, {"n2", "http://h1:1"}, {"n3", "http://h2:1"}}, specs, st, uc, counts, 1, 0, false)
	// n1: a full -> b (c unhealthy). n2: a full, b now full -> note. n3: b full -> note.
	if got[0].to.Name != "b" || got[1].note == "" || got[2].note == "" {
		t.Fatalf("%+v", got)
	}
	// unlimited: everyone stays
	got = planUltraPlacement([]ultraCand{{"n1", "http://h1:1"}}, specs, st, map[string]int{"http://h1:1": 4}, counts, 0, 0, false)
	if got[0].to.ProxyURL != "" || got[0].note != "" {
		t.Fatalf("%+v", got)
	}
	// overall cap respected
	got = planUltraPlacement([]ultraCand{{"n1", "http://h1:1"}}, specs, st, map[string]int{"http://h1:1": 1}, map[string]int{"http://h2:1": 9}, 1, 9, false)
	if got[0].note == "" {
		t.Fatalf("expected note, got %+v", got)
	}
}

func TestEgressRoundPlacesNewUltraOnly(t *testing.T) {
	ctx := context.Background()
	specs := ParseEgressSpecs("a=http://h1:1=1.1.1.1,b=http://h2:1=2.2.2.2")
	manager := coreauth.NewManager(nil, nil, nil)
	now := time.Now().UTC()
	reg := func(id string, created time.Time) {
		md := map[string]any{SubscriptionTierKey: "g1-ultra-tier", "proxy_url": "http://h1:1"}
		if _, err := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: id, Provider: "antigravity", ProxyURL: "http://h1:1", Status: coreauth.StatusActive, CreatedAt: created, Metadata: md}); err != nil {
			t.Fatal(err)
		}
	}
	reg("old1", now.Add(-30*24*time.Hour))
	reg("old2", now.Add(-20*24*time.Hour)) // existing violation: never auto-moved
	reg("new1", now.Add(-time.Hour))
	rt := &Runtime{coreManager: manager}
	old := egressChecker
	egressChecker = func(context.Context, string) error { return nil }
	defer func() { egressChecker = old }()
	rt.egressRound(ctx, specs)
	for id, want := range map[string]string{"old1": "http://h1:1", "old2": "http://h1:1", "new1": "http://h2:1"} {
		a, _ := manager.GetByID(id)
		if a.ProxyURL != want {
			t.Fatalf("%s on %s, want %s", id, a.ProxyURL, want)
		}
	}
	a, _ := manager.GetByID("new1")
	if a.Metadata[EgressUltraPlacedKey] == nil {
		t.Fatal("new1 not marked placed")
	}
	// second round: stable
	rt.egressRound(ctx, specs)
	if a, _ := manager.GetByID("new1"); a.ProxyURL != "http://h2:1" {
		t.Fatal("moved again")
	}
}
