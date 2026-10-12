package cluster

import (
	"context"
	"testing"
	"time"
)

func TestHomeDrainExcludesAndClears(t *testing.T) {
	ctx := context.Background()
	repo, closeRepo := newBillingTestRepository(t, ctx)
	defer closeRepo()
	now := time.Now().UTC()
	nodes := []ClusterNodeRecord{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}
	if err := repo.BeginHomeDrain(ctx, "10.0.0.1", 8327, time.Minute); err != nil {
		t.Fatal(err)
	}
	got := repo.withoutDrainingHomes(ctx, nodes, now)
	if len(got) != 1 || got[0].IP != "10.0.0.2" {
		t.Fatalf("draining home not excluded: %+v", got)
	}
	// Every Home draining: keep the list (proxies need somewhere to go).
	if err := repo.BeginHomeDrain(ctx, "10.0.0.2", 8327, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := repo.withoutDrainingHomes(ctx, nodes, now); len(got) != 2 {
		t.Fatalf("all draining should keep list, got %+v", got)
	}
	// A restart at the same address clears its mark.
	if err := repo.ClearHomeDrain(ctx, "10.0.0.2", 8327); err != nil {
		t.Fatal(err)
	}
	got = repo.withoutDrainingHomes(ctx, nodes, now)
	if len(got) != 1 || got[0].IP != "10.0.0.2" {
		t.Fatalf("after clear: %+v", got)
	}
}
