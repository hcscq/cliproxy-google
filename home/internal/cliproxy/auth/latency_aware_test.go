package auth

import (
	"fmt"
	"testing"
	"time"
)

func latEntries(fast, slow int, fastMs, slowMs float64, now int64) map[string]healthEntry {
	m := map[string]healthEntry{}
	for i := 0; i < fast; i++ {
		m[fmt.Sprintf("fast%d|gemini-2.5", i)] = healthEntry{ttfb: fastMs, n: 20, updated: now}
	}
	for i := 0; i < slow; i++ {
		m[fmt.Sprintf("slow%d|gemini-2.5", i)] = healthEntry{ttfb: slowMs, n: 20, updated: now}
	}
	return m
}

func newLatTracker() *HealthTracker {
	s := DefaultHealthSettings()
	return NewHealthTracker(s, nil)
}

func TestLatencyAwareSlowsMajoritySlowFamily(t *testing.T) {
	tr := newLatTracker()
	now := time.Now().Unix()
	// 60% slow: the median-relative rule cannot see this, the baseline rule must.
	_, penal := tr.peerView(latEntries(8, 12, 1000, 24000, now), now)
	for i := 0; i < 12; i++ {
		if w, ok := penal[fmt.Sprintf("slow%d|gemini-2.5", i)]; !ok || w > 0.1+1e-9 {
			t.Fatalf("slow%d weight=%v ok=%v, want trickle 0.1", i, w, ok)
		}
	}
	for i := 0; i < 8; i++ {
		if _, ok := penal[fmt.Sprintf("fast%d|gemini-2.5", i)]; ok {
			t.Fatalf("fast%d penalised", i)
		}
	}
}

func TestLatencyAwareNeverSlowsWholeFamily(t *testing.T) {
	tr := newLatTracker()
	now := time.Now().Unix()
	// 1 fast, 19 slow: cap at 80% of peers.
	_, penal := tr.peerView(latEntries(1, 19, 1000, 24000, now), now)
	n := 0
	for k := range penal {
		_ = k
		n++
	}
	if n > 16 {
		t.Fatalf("penalised %d of 20, want <= 16", n)
	}
}

func TestLatencyAwareUniformlySlowFamilyUntouched(t *testing.T) {
	tr := newLatTracker()
	now := time.Now().Unix()
	_, penal := tr.peerView(latEntries(0, 20, 0, 24000, now), now)
	if len(penal) != 0 {
		t.Fatalf("uniformly slow family penalised: %d", len(penal))
	}
	// Spread around a slow baseline (upstream-wide 3.8-like) is not slowed by the absolute rule.
	m := latEntries(10, 10, 9000, 14000, now)
	_, penal = tr.peerView(m, now)
	if len(penal) != 0 {
		t.Fatalf("slow-baseline family penalised by absolute rule: %d", len(penal))
	}
}

func TestLatencyAwareDisabled(t *testing.T) {
	s := DefaultHealthSettings()
	s.LatencyAware = false
	tr := NewHealthTracker(s, nil)
	now := time.Now().Unix()
	_, penal := tr.peerView(latEntries(8, 12, 1000, 24000, now), now)
	for k := range penal {
		if k[:4] == "slow" && penal[k] <= 0.1 {
			t.Fatalf("latency penalty applied while disabled: %s", k)
		}
	}
}

func TestLatencyAwareTooFewPeers(t *testing.T) {
	tr := newLatTracker()
	now := time.Now().Unix()
	_, penal := tr.peerView(latEntries(1, 2, 1000, 24000, now), now)
	if len(penal) != 0 {
		t.Fatalf("penalised with too few peers")
	}
}
