package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

func TestWeightedRoundRobinAvailabilityAndDistribution(t *testing.T) {
	ctx := context.Background()
	a := &Auth{ID: "a", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "3"}}
	b := &Auth{ID: "b", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"weight": 1}}
	zero := &Auth{ID: "zero", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "0", "priority": "100"}}
	invalid := &Auth{ID: "invalid", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "bad", "priority": "100"}}
	selector := &WeightedRoundRobinSelector{}
	counts := map[string]int{}
	for range 40 {
		picked, err := selector.Pick(ctx, "codex", "model", Options{}, []*Auth{nil, a, b, zero, invalid})
		if err != nil {
			t.Fatal(err)
		}
		counts[picked.ID]++
	}
	if counts["a"] != 30 || counts["b"] != 10 || len(counts) != 2 {
		t.Fatalf("3:1 distribution = %v", counts)
	}
	if _, err := selector.Pick(ctx, "codex", "model", Options{}, []*Auth{zero, invalid, nil}); err == nil {
		t.Fatal("zero-weight pool was dispatchable")
	}
	a.ModelStates = map[string]*ModelState{"model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}
	picked, err := selector.Pick(ctx, "codex", "model", Options{}, []*Auth{a, b, zero})
	if err != nil || picked.ID != "b" {
		t.Fatalf("cooldown fallback = %v %v", picked, err)
	}
}

func TestWeightedSessionAffinityDropsZeroWeightBinding(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &WeightedRoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	a := &Auth{ID: "a", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "3"}}
	b := &Auth{ID: "b", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "1"}}
	opts := Options{Headers: http.Header{"X-Session-ID": []string{"weighted-session"}}}
	picked, err := selector.Pick(context.Background(), "codex", "model", opts, []*Auth{a, b})
	if err != nil || picked.ID != "a" {
		t.Fatalf("initial binding = %v %v", picked, err)
	}
	a.Attributes["weight"] = "0"
	picked, err = selector.Pick(context.Background(), "codex", "model", opts, []*Auth{a, b})
	if err != nil || picked.ID != "b" {
		t.Fatalf("zero-weight binding retained = %v %v", picked, err)
	}
}

func TestWeightedDispatchPreservesCreditsAcrossRetryExclusions(t *testing.T) {
	manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
	a := &Auth{ID: "weighted-a", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "3"}}
	b := &Auth{ID: "weighted-b", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "1"}}
	zero := &Auth{ID: "weighted-zero", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "0", "priority": "100"}}
	for _, auth := range []*Auth{a, b, zero} {
		registerDispatchTestAuth(t, manager, auth, "model")
	}
	counts := map[string]int{}
	for range 40 {
		decision, err := manager.Dispatch(context.Background(), []string{"codex"}, "model", Options{})
		if err != nil {
			t.Fatal(err)
		}
		counts[decision.Auth.ID]++
		retry, errRetry := manager.Dispatch(context.Background(), []string{"codex"}, "model", Options{Metadata: map[string]any{ExcludedAuthIDsMetadataKey: []string{decision.Auth.ID}}})
		if errRetry != nil || retry.Auth.ID == decision.Auth.ID || retry.Auth.ID == zero.ID {
			t.Fatalf("retry = %v %v", retry, errRetry)
		}
	}
	if counts[a.ID] != 30 || counts[b.ID] != 10 {
		t.Fatalf("retry exclusions reset weighted credits: %v", counts)
	}
}

func TestWeightedDispatchAliasCooldownAndSessionAffinity(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		var selector Selector = &WeightedRoundRobinSelector{}
		if affinity {
			bound := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: selector, TTL: time.Hour})
			defer bound.Stop()
			selector = bound
		}
		manager := NewManager(nil, selector, nil)
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"antigravity": {{Name: "upstream", Alias: "alias", ForceMapping: true}}})
		a := &Auth{ID: "alias-a", Provider: "antigravity", Status: StatusActive, Attributes: map[string]string{"weight": "100", "auth_kind": "oauth"}, ModelStates: map[string]*ModelState{"upstream": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}}
		b := &Auth{ID: "alias-b", Provider: "antigravity", Status: StatusActive, Attributes: map[string]string{"weight": "1", "auth_kind": "oauth"}}
		registerDispatchTestAuth(t, manager, a, "alias")
		registerDispatchTestAuth(t, manager, b, "alias")
		opts := Options{Headers: http.Header{"X-Session-ID": []string{"alias-session"}}}
		decision, err := manager.Dispatch(context.Background(), []string{"antigravity"}, "alias", opts)
		if err != nil || decision.Auth.ID != b.ID || decision.UpstreamModel != "upstream" || !decision.ForceMapping {
			t.Fatalf("affinity=%v alias dispatch=%+v err=%v", affinity, decision, err)
		}
	}
}
