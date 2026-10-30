package home

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

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
	got, ok := pickEgress(specs, status, map[string]int{"http://h1:1": 2, "http://h2:1": 1}, true)
	if !ok || got.Name != "b" {
		t.Fatalf("pick = %+v %v", got, ok)
	}
	status["http://h1:1"].Healthy, status["http://h2:1"].Healthy = false, false
	if _, ok := pickEgress(specs, status, nil, true); ok {
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
	egressChecker = func(_ context.Context, u string, _ time.Duration) error {
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

func TestPickEgressSecondaryOnly(t *testing.T) {
	specs := []EgressSpec{{Name: "n/1", ProxyURL: "u1", Primary: true}, {Name: "n/2", ProxyURL: "u2"}}
	status := map[string]*EgressStatus{"u1": {Healthy: true}, "u2": {Healthy: true}}
	counts := map[string]int{"u2": 5}
	if got, _ := pickEgress(specs, status, counts, false); got.Name != "n/2" {
		t.Fatalf("secondary-only picked %q", got.Name)
	}
	if got, _ := pickEgress(specs, status, counts, true); got.Name != "n/1" {
		t.Fatalf("allow primary picked %q", got.Name)
	}
	status["u2"].Healthy = false
	if _, ok := pickEgress(specs, status, counts, false); ok {
		t.Fatal("must not fall back to primary")
	}
}

func TestPickEgressRespectsMaxPerEgress(t *testing.T) {
	specs := []EgressSpec{{Name: "a", ProxyURL: "http://a"}, {Name: "b", ProxyURL: "http://b"}}
	counts := map[string]int{"http://a": 2, "http://b": 3}
	if s, ok := pickEgress(specs, nil, counts, false, 3); !ok || s.Name != "a" {
		t.Fatalf("want a, got %v %v", s.Name, ok)
	}
	counts["http://a"] = 3
	if _, ok := pickEgress(specs, nil, counts, false, 3); ok {
		t.Fatal("all full: want no egress")
	}
	if _, ok := pickEgress(specs, nil, counts, false, 0); !ok {
		t.Fatal("0 = unlimited")
	}
}


func TestEgressProbeSettingsDefaults(t *testing.T) {
	var z EgressSettingsValue
	if z.ProbeTick() != egressTick || z.EffectiveFailThreshold() != egressFailThreshold || z.ProbeTimeout() != egressCheckTimeout || z.EffectiveProbePerAgent() != egressDefaultPerAgent {
		t.Fatalf("zero defaults: tick=%v fail=%d timeout=%v per=%d", z.ProbeTick(), z.EffectiveFailThreshold(), z.ProbeTimeout(), z.EffectiveProbePerAgent())
	}
	d := z.WithProbeDefaults()
	if d.ProbeTickSeconds != 5 || d.FailThreshold != 6 || d.ProbeTimeoutSeconds != 8 || d.ProbePerAgent != 1 {
		t.Fatalf("with defaults: %+v", d)
	}
	custom := EgressSettingsValue{ProbeTickSeconds: 1, FailThreshold: 100, ProbeTimeoutSeconds: 1, ProbePerAgent: 99}
	if custom.ProbeTick() != 2*time.Second || custom.EffectiveFailThreshold() != 30 || custom.ProbeTimeout() != 2*time.Second || custom.EffectiveProbePerAgent() != 8 {
		t.Fatalf("clamps: tick=%v fail=%d timeout=%v per=%d", custom.ProbeTick(), custom.EffectiveFailThreshold(), custom.ProbeTimeout(), custom.EffectiveProbePerAgent())
	}
}

func TestAgentHostOf(t *testing.T) {
	if got := agentHostOf("http://1.2.3.4@10.0.0.1:3141"); got != "10.0.0.1:3141" {
		t.Fatalf("got %q", got)
	}
	if got := agentHostOf("http://10.0.0.2:3141"); got != "10.0.0.2:3141" {
		t.Fatalf("got %q", got)
	}
}

func TestEgressRoundNeedsFailThreshold(t *testing.T) {
	ctx := context.Background()
	specs := ParseEgressSpecs("a=http://h1:1=1.1.1.1")
	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: "c1", Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{}})
	rt := &Runtime{coreManager: manager}
	old := egressChecker
	egressChecker = func(_ context.Context, _ string, _ time.Duration) error { return errors.New("down") }
	defer func() { egressChecker = old }()
	// Below threshold: still healthy, credential not paused.
	for i := 0; i < egressFailThreshold-1; i++ {
		rt.egressRound(ctx, specs)
	}
	st := rt.egress.status["http://h1:1"]
	if st == nil || !st.Healthy || st.Failures != egressFailThreshold-1 {
		t.Fatalf("pre-threshold status = %+v", st)
	}
	c1, _ := manager.GetByID("c1")
	if c1.Disabled {
		t.Fatal("paused before fail threshold")
	}
	rt.egressRound(ctx, specs)
	st = rt.egress.status["http://h1:1"]
	if st.Healthy {
		t.Fatal("still healthy after threshold")
	}
}
