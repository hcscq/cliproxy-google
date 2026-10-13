package home

import (
	"context"
	"errors"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

type fakeIP struct{ ip, public string }

func setAgents(t *testing.T, inv map[string][]fakeIP, down map[string]bool) {
	t.Helper()
	t.Setenv(egressAgentsEnv, "a1:3141,a2:3141")
	resolveAgents = func(context.Context, string) []string { return []string{"a1:3141", "a2:3141"} }
	fetchInventory = func(_ context.Context, agent string) (agentInventory, error) {
		node := map[string]string{"a1:3141": "rainyun-n1", "a2:3141": "rainyun-n2"}[agent]
		if down[node] {
			return agentInventory{}, errors.New("unreachable")
		}
		out := agentInventory{Node: node}
		for _, f := range inv[node] {
			out.IPs = append(out.IPs, struct {
				IP         string    `json:"ip"`
				Primary    bool      `json:"primary"`
				PublicIP   string    `json:"public_ip"`
				ProbedAt   time.Time `json:"probed_at"`
				ProbeError string    `json:"probe_error"`
			}{IP: f.ip, PublicIP: f.public})
		}
		return out, nil
	}
}

func TestEgressDiscoveryLifecycle(t *testing.T) {
	ctx := context.Background()
	t.Setenv(egressEnv, "egress-n1=http://a1:3140=1.1.1.2")
	inv := map[string][]fakeIP{"rainyun-n1": {{"1.1.1.1", "1.1.1.1"}, {"1.1.1.2", "1.1.1.2"}}, "rainyun-n2": {{"2.2.2.2", "2.2.2.2"}}}
	down := map[string]bool{}
	setAgents(t, inv, down)
	old := egressChecker
	egressChecker = func(context.Context, string) error { return nil }
	defer func() { egressChecker = old }()

	manager := coreauth.NewManager(nil, nil, nil)
	// legacy squid pin on 1.1.1.2 must be repointed to the agent with the same IP.
	_, _ = manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: "legacy", Provider: "antigravity", ProxyURL: "http://a1:3140", Status: coreauth.StatusActive, Metadata: map[string]any{}})
	_, _ = manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: "new", Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{}})
	rt := &Runtime{coreManager: manager}
	round := func() {
		specs, legacy := rt.currentEgressSpecs(ctx, true)
		rt.egressRoundWith(ctx, specs, legacy)
	}
	round()
	specs, _ := rt.currentEgressSpecs(ctx, false)
	if len(specs) != 3 || specs[0].Name != "n1/1.1.1.1" || specs[0].ProxyURL != "http://1.1.1.1@a1:3141" {
		t.Fatalf("specs = %+v", specs)
	}
	lg, _ := manager.GetByID("legacy")
	if lg.ProxyURL != "http://1.1.1.2@a1:3141" || lg.Metadata[EgressIDKey] != "n1/1.1.1.2" {
		t.Fatalf("legacy not repointed to same IP: %q %v", lg.ProxyURL, lg.Metadata)
	}
	nw, _ := manager.GetByID("new")
	if nw.ProxyURL == "" || nw.ProxyURL == lg.ProxyURL {
		t.Fatalf("new cred should go to a least-loaded egress, got %q", nw.ProxyURL)
	}

	// IP removed on a reachable node -> its credential is paused, never moved.
	inv["rainyun-n1"] = []fakeIP{{"1.1.1.1", "1.1.1.1"}}
	round()
	lg, _ = manager.GetByID("legacy")
	if !lg.Disabled || lg.Metadata[EgressPausedReasonKey] != egressPausedRemoved || lg.ProxyURL != "http://1.1.1.2@a1:3141" {
		t.Fatalf("removed IP: %+v", lg)
	}
	// Agent unreachable: entries kept (not removed); health decides.
	down["rainyun-n2"] = true
	specs, _ = rt.currentEgressSpecs(ctx, true)
	for _, s := range specs {
		if s.Node == "rainyun-n2" && s.Removed {
			t.Fatal("unreachable agent must not mark IPs removed")
		}
	}
	down["rainyun-n2"] = false
	// IP back -> resumed on the same egress.
	inv["rainyun-n1"] = []fakeIP{{"1.1.1.1", "1.1.1.1"}, {"1.1.1.2", "1.1.1.2"}}
	round()
	lg, _ = manager.GetByID("legacy")
	if lg.Disabled || lg.ProxyURL != "http://1.1.1.2@a1:3141" {
		t.Fatalf("not resumed: %+v", lg)
	}
	// Public IP change is flagged, credential keeps running.
	inv["rainyun-n2"] = []fakeIP{{"2.2.2.2", "9.9.9.9"}}
	round()
	specs, _ = rt.currentEgressSpecs(ctx, false)
	var n2 EgressSpec
	for _, s := range specs {
		if s.Name == "n2/2.2.2.2" {
			n2 = s
		}
	}
	if !n2.PublicChanged || n2.ExpectedPublicIP != "2.2.2.2" {
		t.Fatalf("public change not flagged: %+v", n2)
	}
	if err := rt.AckEgressPublicIP(ctx, "n2/2.2.2.2"); err != nil {
		t.Fatal(err)
	}
	specs, _ = rt.currentEgressSpecs(ctx, true)
	for _, s := range specs {
		if s.Name == "n2/2.2.2.2" && s.PublicChanged {
			t.Fatal("ack did not clear flag")
		}
	}
}
