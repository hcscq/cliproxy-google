package auth

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

func localHealth() *HealthTracker {
	return NewHealthTracker(DefaultHealthSettings(), nil)
}

func TestHealthFamilySharesTiersNotVersions(t *testing.T) {
	if HealthFamily("gemini-3.8-flash-tiered") != HealthFamily("gemini-3.8-pro-high") {
		t.Fatal("3.8 tiers must share a family")
	}
	if HealthFamily("gemini-3.8-flash") == HealthFamily("gemini-2.5-flash") {
		t.Fatal("2.5 must be separate")
	}
	if HealthLabel(HealthFamily("gemini-3.8-flash")) != "3.8" {
		t.Fatal(HealthLabel(HealthFamily("gemini-3.8-flash")))
	}
}

func TestHealthSlowQuarantineEscalatesAndResets(t *testing.T) {
	h := localHealth()
	o := HealthObservation{AuthID: "a", Model: "gemini-3.8-flash", Slow: true, Failed: true, TTFBMs: 75000}
	if cd := h.Observe(context.Background(), o); cd != 15*time.Second {
		t.Fatalf("sub-threshold: %s", cd)
	}
	if cd := h.Observe(context.Background(), o); cd != 5*time.Minute {
		t.Fatalf("first quarantine: %s", cd)
	}
	h.Observe(context.Background(), o)
	if cd := h.Observe(context.Background(), o); cd != 10*time.Minute {
		t.Fatalf("escalation: %s", cd)
	}
	// 2.5 on the same credential is untouched.
	if w := h.Weight("a", "gemini-2.5-flash"); w != 1 {
		t.Fatalf("2.5 weight %v", w)
	}
	h.Observe(context.Background(), HealthObservation{AuthID: "a", Model: "gemini-3.8-flash", Success: true, TTFBMs: 900})
	h.Observe(context.Background(), o)
	if cd := h.Observe(context.Background(), o); cd != 5*time.Minute {
		t.Fatalf("reset after success: %s", cd)
	}
}

func TestHealthCapacitySteps(t *testing.T) {
	h := localHealth()
	o := HealthObservation{AuthID: "a", Model: "gemini-3.8-flash", Capacity: true, Failed: true}
	for i, w := range []time.Duration{time.Second, 10 * time.Second, time.Minute, time.Minute} {
		if cd := h.Observe(context.Background(), o); cd != w {
			t.Fatalf("step %d: %s", i, cd)
		}
	}
}

func TestHealthSoftWeightAndProbeFloor(t *testing.T) {
	h := localHealth()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		for j := 0; j < 5; j++ {
			h.Observe(ctx, HealthObservation{AuthID: fmt.Sprint("good", j), Model: "gemini-3.8-flash", Success: true, TTFBMs: 2000})
		}
		h.Observe(ctx, HealthObservation{AuthID: "slow", Model: "gemini-3.8-flash", Success: true, TTFBMs: 20000})
		h.Observe(ctx, HealthObservation{AuthID: "bad", Model: "gemini-3.8-flash", Failed: true})
	}
	h.sync(ctx)
	if w := h.Weight("good0", "gemini-3.8-flash"); w != 1 {
		t.Fatalf("good weight %v", w)
	}
	ws := h.Weight("slow", "gemini-3.8-flash")
	if ws > 0.2 || ws < 0.15 {
		t.Fatalf("slow weight %v", ws)
	}
	if wb := h.Weight("bad", "gemini-3.8-flash"); wb != 0.15 {
		t.Fatalf("bad weight %v (probe floor)", wb)
	}
	admitted := 0
	for i := 0; i < 10000; i++ {
		if h.Admit("bad", "gemini-3.8-flash") {
			admitted++
		}
	}
	if admitted < 1200 || admitted > 1800 {
		t.Fatalf("probe traffic %d/10000", admitted)
	}
	st := h.Status("bad")
	if len(st) != 1 || st[0].Label != "3.8" || st[0].State != "降级" {
		t.Fatalf("status %+v", st)
	}
	if len(h.Status("good0")) != 0 {
		t.Fatal("good should have no status")
	}
}

func TestHealthBounded(t *testing.T) {
	s := DefaultHealthSettings()
	s.MaxEntries = 1000
	h := NewHealthTracker(s, nil)
	for i := 0; i < 20000; i++ {
		h.Observe(context.Background(), HealthObservation{AuthID: fmt.Sprint(i), Model: "gemini-3.8-flash", Success: true, TTFBMs: 1})
	}
	if len(h.entries) > 1000 || len(h.byAuth) > 1000 {
		t.Fatalf("entries %d index %d", len(h.entries), len(h.byAuth))
	}
}

// TestHealthRedisClusterWide runs against a real Redis (HEALTH_TEST_REDIS=host:port).
func TestHealthRedisClusterWide(t *testing.T) {
	addr := os.Getenv("HEALTH_TEST_REDIS")
	if addr == "" {
		t.Skip("HEALTH_TEST_REDIS not set")
	}
	c := redisx.Open(redisx.Config{Enabled: true, Addr: addr, KeyPrefix: fmt.Sprintf("t%d:", time.Now().UnixNano())})
	defer c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !c.Healthy() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	rc := func() *redisx.Client { return c }
	podA := NewHealthTracker(DefaultHealthSettings(), rc)
	podB := NewHealthTracker(DefaultHealthSettings(), rc)
	o := HealthObservation{AuthID: "x", Model: "gemini-3.8-flash", Slow: true, Failed: true, TTFBMs: 75000}
	if cd := podA.Observe(context.Background(), o); cd != 15*time.Second {
		t.Fatalf("A first: %s", cd)
	}
	// The second slow event lands on another replica and still crosses the shared threshold.
	if cd := podB.Observe(context.Background(), o); cd != 5*time.Minute {
		t.Fatalf("B second (shared threshold): %s", cd)
	}
	for _, step := range []time.Duration{time.Second, 10 * time.Second} {
		if cd := podA.Observe(context.Background(), HealthObservation{AuthID: "x", Model: "gemini-3.8-pro", Capacity: true, Failed: true}); cd != step {
			t.Fatalf("capacity %s", cd)
		}
	}
	podB.sync(context.Background())
	if _, ok := podB.entries[healthKey("x", "gemini-3.8")]; !ok {
		t.Fatal("B mirror missing pair after sync")
	}
}
