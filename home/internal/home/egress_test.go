package home

import (
	"context"
	"errors"
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestParseEgressSpecs(t *testing.T) {
	got := ParseEgressSpecs("a=http://10.0.0.1:3140=1.1.1.1, bad, b=http://10.0.0.2:3140=2.2.2.2,c=::=3.3.3.3")
	if len(got) != 2 || got[0].Name != "a" || got[1].IP != "2.2.2.2" {
		t.Fatalf("specs = %+v", got)
	}
}

func TestPickEgressLeastLoadedHealthy(t *testing.T) {
	specs := ParseEgressSpecs("a=http://h1:1=1.1.1.1,b=http://h2:1=2.2.2.2,c=http://h3:1=3.3.3.3")
	status := map[string]*EgressStatus{
		"http://h1:1": {Healthy: true}, "http://h2:1": {Healthy: true}, "http://h3:1": {Healthy: false},
	}
	got, ok := pickEgress(specs, status, map[string]int{"http://h1:1": 2, "http://h2:1": 1})
	if !ok || got.Name != "b" {
		t.Fatalf("pick = %+v %v", got, ok)
	}
	status["http://h1:1"].Healthy, status["http://h2:1"].Healthy = false, false
	if _, ok := pickEgress(specs, status, nil); ok {
		t.Fatal("picked an unhealthy egress")
	}
}

func TestEgressRoundAssignPauseResume(t *testing.T) {
	ctx := context.Background()
	specs := ParseEgressSpecs("a=http://h1:1=1.1.1.1,b=http://h2:1=2.2.2.2")
	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"c1", "c2", "c3"} {
		if _, err := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: id, Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	manual := &coreauth.Auth{ID: "m", Provider: "antigravity", ProxyURL: "http://manual:1", Status: coreauth.StatusActive, Metadata: map[string]any{}}
	_, _ = manager.Register(coreauth.WithSkipPersist(ctx), manual)
	rt := &Runtime{coreManager: manager}
	down := map[string]bool{}
	old := egressChecker
	egressChecker = func(_ context.Context, u string) error {
		if down[u] {
			return errors.New("down")
		}
		return nil
	}
	defer func() { egressChecker = old }()

	rt.egressRound(ctx, specs)
	counts := map[string]int{}
	for _, id := range []string{"c1", "c2", "c3"} {
		a, _ := manager.GetByID(id)
		if a.ProxyURL == "" || a.Metadata[EgressIPKey] == nil {
			t.Fatalf("%s not pinned: %+v", id, a)
		}
		counts[a.ProxyURL]++
	}
	if counts["http://h1:1"] != 2 || counts["http://h2:1"] != 1 {
		t.Fatalf("unbalanced: %v", counts)
	}
	if m, _ := manager.GetByID("m"); m.ProxyURL != "http://manual:1" {
		t.Fatal("manual proxy overwritten")
	}
	c2, _ := manager.GetByID("c2")
	pinned := c2.ProxyURL

	down[pinned] = true
	for i := 0; i < egressFailThreshold; i++ {
		rt.egressRound(ctx, specs)
	}
	c2, _ = manager.GetByID("c2")
	if !c2.Disabled || c2.Metadata[EgressPausedReasonKey] != egressPausedDown || c2.ProxyURL != pinned {
		t.Fatalf("not paused in place: %+v", c2)
	}
	down[pinned] = false
	rt.egressRound(ctx, specs)
	c2, _ = manager.GetByID("c2")
	if c2.Disabled || c2.ProxyURL != pinned {
		t.Fatalf("not resumed on same egress: %+v", c2)
	}
}

func TestCredentialHTTPClientFailClosed(t *testing.T) {
	c := credentialHTTPClient(&coreauth.Auth{ProxyURL: "::bad"}, 0)
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("bad proxy must fail closed, not go direct")
	}
}

func TestApplyEgressMetadata(t *testing.T) {
	a := &coreauth.Auth{Disabled: true, Metadata: map[string]any{EgressPausedReasonKey: egressPausedDown}}
	ApplyEgressMetadata(a, EgressSpec{Name: "e1", ProxyURL: "http://h:1", IP: "1.2.3.4"})
	if a.ProxyURL != "http://h:1" || a.Metadata[EgressIPKey] != "1.2.3.4" || a.Disabled {
		t.Fatalf("pin/unpause failed: %+v", a)
	}
	ApplyEgressMetadata(a, EgressSpec{})
	if a.ProxyURL != "" || a.Metadata[EgressIDKey] != nil {
		t.Fatalf("auto unpin failed: %+v", a)
	}
}
