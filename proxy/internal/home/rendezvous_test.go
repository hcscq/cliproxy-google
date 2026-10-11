package home

import (
	"fmt"
	"testing"
)

func TestRendezvousOrderSpreadsAndIsStable(t *testing.T) {
	nodes := []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}, {IP: "10.0.0.3", Port: 8327}}
	counts := map[string]int{}
	first := map[string]string{}
	for i := 0; i < 3000; i++ {
		seed := fmt.Sprintf("proxy-%d", i)
		order := rendezvousOrder(append([]clusterNode(nil), nodes...), seed)
		if len(order) != 3 {
			t.Fatalf("order lost nodes: %v", order)
		}
		again := rendezvousOrder([]clusterNode{nodes[2], nodes[0], nodes[1]}, seed) // input order must not matter
		if again[0] != order[0] || again[1] != order[1] {
			t.Fatalf("unstable order for %s", seed)
		}
		counts[order[0].IP]++
		first[seed] = order[0].IP
	}
	for ip, n := range counts {
		if n < 850 || n > 1150 {
			t.Fatalf("uneven spread %s=%d (%v)", ip, n, counts)
		}
	}
	// Removing a node only moves the proxies that were on it.
	for i := 0; i < 3000; i++ {
		seed := fmt.Sprintf("proxy-%d", i)
		order := rendezvousOrder([]clusterNode{nodes[0], nodes[1]}, seed)
		if first[seed] != "10.0.0.3" && order[0].IP != first[seed] {
			t.Fatalf("proxy %s moved off a surviving node", seed)
		}
	}
}

func TestRendezvousOrderEdgeCases(t *testing.T) {
	if got := rendezvousOrder([]clusterNode{{IP: "a", Port: 1}}, "s"); len(got) != 1 {
		t.Fatal("single node")
	}
	two := []clusterNode{{IP: "a", Port: 1}, {IP: "b", Port: 1}}
	if got := rendezvousOrder(two, ""); got[0].IP != "a" {
		t.Fatal("empty seed must keep input order")
	}
}
