package home

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const twoHomes = `{"ok":true,"nodes":[{"ip":"10.0.0.1","port":8327},{"ip":"10.0.0.2","port":8327}]`

func TestOrderClusterNodesPutsAssignedFirstElseRendezvous(t *testing.T) {
	nodes := []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}, {IP: "10.0.0.3", Port: 8327}}
	base := rendezvousOrder(nodes, "seed")
	if got := orderClusterNodes(nodes, nil, "seed"); got[0] != base[0] {
		t.Fatal("no assignment must keep rendezvous order")
	}
	want := base[len(base)-1]
	got := orderClusterNodes(nodes, &want, "seed")
	if got[0] != want || len(got) != 3 {
		t.Fatalf("assigned not first: %+v", got)
	}
	dead := clusterNode{IP: "10.9.9.9", Port: 8327}
	if got := orderClusterNodes(nodes, &dead, "seed"); got[0] != base[0] {
		t.Fatal("an assignment to a non-live home must be ignored")
	}
}

func TestClusterPushTriggersMoveOnlyWhenNeeded(t *testing.T) {
	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	// Assigned to the current Home: no move.
	if err := c.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.1","port":8327}}`)); err != nil {
		t.Fatal(err)
	}
	if c.takeReassignPending() {
		t.Fatal("moved while already on the assigned home")
	}
	// Assigned elsewhere: move once.
	if err := c.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.2","port":8327}}`)); err != nil {
		t.Fatal(err)
	}
	if !c.takeReassignPending() || c.clusterNodes[0].IP != "10.0.0.2" {
		t.Fatal("expected a move to the assigned home")
	}
	// Rate limit: another push right away does not move again.
	_ = c.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.2","port":8327}}`))
	if c.takeReassignPending() {
		t.Fatal("reassignment must be rate limited")
	}
	// After the interval it may move; a fresh lifetime keeps the timestamp.
	c.lastReassignAt = time.Now().Add(-2 * homeReassignMinInterval)
	next := c.NewLifetime()
	_ = next.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.2","port":8327}}`))
	if !next.takeReassignPending() {
		t.Fatal("expected a move after the interval")
	}
	// Assigned Home not live: stay.
	c2 := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	_ = c2.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.9","port":8327}}`))
	if c2.takeReassignPending() {
		t.Fatal("must not move to a home missing from the live list")
	}
	// Old Home (no assignment field): unchanged rendezvous behaviour.
	c3 := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	_ = c3.updateClusterNodesFromPayload([]byte(twoHomes + `}`))
	if c3.takeReassignPending() {
		t.Fatal("no assignment must never move")
	}
}

// Regression (rollout 2026-10-11): two dead Homes at the head of the list made
// failover alternate between them forever.
func TestFailoverWalksAllNodesThenSeed(t *testing.T) {
	c := New(config.HomeConfig{Host: "seed.svc", Port: 8327})
	c.clusterNodes = []clusterNode{{IP: "dead1", Port: 8327}, {IP: "dead2", Port: 8327}, {IP: "live1", Port: 8327}}
	c.homeCfg.Host, c.homeCfg.Port = "dead1", 8327
	var visited []string
	for i := 0; i < 4; i++ {
		c.mu.Lock()
		_, addr := c.switchToNextNodeLocked()
		c.mu.Unlock()
		visited = append(visited, addr)
	}
	want := []string{"dead2:8327", "live1:8327", "seed.svc:8327", "dead1:8327"}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("failover order %v, want %v", visited, want)
		}
	}
}
