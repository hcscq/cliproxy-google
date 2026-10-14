package auth

import (
	"context"
	"testing"
	"time"
)

// A credential whose TTFB p95 is far above the absolute threshold is demoted
// even when its mean is only moderately slow; a live settings change applies.
func TestHealthAbsoluteDemotionP95AndLiveSettings(t *testing.T) {
	h := localHealth()
	healthyPeers(h, 20)
	ctx := context.Background()
	// Mostly 8s, every 5th 60s: mean ~18s (below 2x-peer + extra? peers 2s → relative slow too),
	// so isolate the absolute rule by disabling the relative one.
	s := h.Settings()
	s.SlowFactor = 100
	h.SetSettings(s)
	for i := 0; i < 40; i++ {
		ttfb := int64(8000)
		if i%5 == 0 {
			ttfb = 60000
		}
		h.Observe(ctx, HealthObservation{AuthID: "slow", Model: "gemini-3.8-flash", Success: true, TTFBMs: ttfb})
	}
	h.sync(ctx)
	if w := h.Weight("slow", "gemini-3.8-flash"); w != demotedWeight {
		t.Fatalf("p95 demotion weight = %v", w)
	}
	s.DemoteTTFBP95 = 0
	h.SetSettings(s)
	h.sync(ctx)
	if w := h.Weight("slow", "gemini-3.8-flash"); w != 1 {
		t.Fatalf("disabled threshold must restore weight, got %v", w)
	}
}

func TestHealthAbsoluteDemotionFailRateRespectsGuards(t *testing.T) {
	h := localHealth()
	healthyPeers(h, 9)
	ctx := context.Background()
	s := h.Settings()
	s.FailMargin = 0.9 // isolate the absolute rule
	h.SetSettings(s)
	for i := 0; i < 20; i++ {
		h.Observe(ctx, HealthObservation{AuthID: "bad", Model: "gemini-3.8-flash", Failed: i%2 == 0, Success: i%2 == 1, TTFBMs: 2000})
	}
	h.sync(ctx)
	if w := h.Weight("bad", "gemini-3.8-flash"); w != demotedWeight {
		t.Fatalf("fail demotion weight = %v", w)
	}
	// Upstream-wide (family median fail high) → nobody is demoted.
	s.UpstreamTTFB = time.Millisecond
	h.SetSettings(s)
	h.sync(ctx)
	if w := h.Weight("bad", "gemini-3.8-flash"); w != 1 {
		t.Fatalf("upstream-wide guard: %v", w)
	}
	// Share cap: 0% demotable.
	s.UpstreamTTFB, s.MaxDegradedFrac = 45*time.Second, 0
	h.SetSettings(s)
	h.sync(ctx)
	if w := h.Weight("bad", "gemini-3.8-flash"); w != 1 {
		t.Fatalf("share cap: %v", w)
	}
}

func TestHealthQ95Converges(t *testing.T) {
	q, mean := 0.0, 0.0
	for i := 0; i < 2000; i++ {
		x := float64(1000 + (i*37)%100*100) // uniform 1s..10.9s
		if mean == 0 {
			mean = x
		} else {
			mean += 0.2 * (x - mean)
		}
		q = healthQ95Step(q, x, mean, 0.2)
	}
	if q < 8000 || q > 12500 {
		t.Fatalf("q95 = %v, want ~10.4s", q)
	}
}

func TestHealthEntryDecodeOldFormat(t *testing.T) {
	e, ok := decodeHealthEntry("100.0,0.1000,5,1,0,0,0,0")
	if !ok || e.q95 != 0 || e.n != 5 {
		t.Fatalf("old format: %+v %v", e, ok)
	}
	e.q95 = 1234.5
	if d, _ := decodeHealthEntry(e.encode()); d.q95 != 1234.5 {
		t.Fatalf("roundtrip q95 = %v", d.q95)
	}
}
