package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestPlanUltraRebalance(t *testing.T) {
	items := []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "a", ProxyURL: "u1", IP: "1"}, Healthy: true},
		{EgressSpec: home.EgressSpec{Name: "b", ProxyURL: "u2", IP: "2"}, Healthy: true},
		{EgressSpec: home.EgressSpec{Name: "p", ProxyURL: "u3", IP: "3", Primary: true}, Healthy: true},
	}
	now := time.Now()
	mk := func(id, url, tier string, age int) *coreauth.Auth {
		return &coreauth.Auth{ID: id, Provider: "antigravity", ProxyURL: url, CreatedAt: now.Add(-time.Duration(age) * time.Hour), Metadata: map[string]any{"subscription_tier": tier}}
	}
	auths := []*coreauth.Auth{mk("x1", "u1", "g1-ultra-tier", 30), mk("x2", "u1", "g1-ultra-tier", 20), mk("x3", "u1", "g1-ultra-tier", 10), mk("pro", "u2", "g1-pro-tier", 5)}
	moves := planUltraRebalance(items, auths, 1, 0, false)
	// oldest x1 stays; x3 (newest) -> b; x2 has no room (primary not allowed)
	if len(moves) != 1 || moves[0].CredentialID != "x3" || moves[0].To != "b" {
		t.Fatalf("%+v", moves)
	}
	moves = planUltraRebalance(items, auths, 1, 0, true)
	if len(moves) != 2 {
		t.Fatalf("%+v", moves)
	}
	if planUltraRebalance(items, auths, 0, 0, true) != nil {
		t.Fatal("unlimited should not move")
	}
}
