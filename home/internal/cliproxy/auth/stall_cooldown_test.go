package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A single first-byte timeout must cool the (credential, model) pair for the
// stall cooldown, also when the family is upstream-wide slow (guard path).
func TestStallCooldownSingleTimeout(t *testing.T) {
	s := DefaultHealthSettings()
	s.SubThresholdCool = 45 * time.Second
	h := NewHealthTracker(s, nil)
	healthyPeers(h, 20)
	o := HealthObservation{AuthID: "a", Model: "gemini-3.8-flash-tiered", Slow: true, Failed: true, TTFBMs: 15000}
	if cd := h.Observe(context.Background(), o); cd != 45*time.Second {
		t.Fatalf("single stall: %s", cd)
	}
	// Upstream-wide slow family: quarantine softened to the stall cooldown, never below.
	up := NewHealthTracker(s, nil)
	for i := 0; i < 3; i++ {
		if cd := up.Observe(context.Background(), o); cd != 45*time.Second {
			t.Fatalf("no peers stall %d: %s", i, cd)
		}
	}
}

// The cooldown lands on that model only; other models of the credential stay usable.
func TestStallCooldownScopedToModel(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	a := &Auth{ID: "c1", Provider: "antigravity", Status: StatusActive}
	cd := 45 * time.Second
	m.applyResultTransition(a, Result{AuthID: "c1", Provider: "antigravity", Model: "gemini-3.8-flash-tiered", RetryAfter: &cd,
		Error: &Error{HTTPStatus: http.StatusGatewayTimeout, Message: `{"error":{"code":504,"message":"3.8 慢: no first byte from upstream within 15s"}}`}}, "gemini-3.8-flash-tiered", now, false, 0)
	st := a.ModelStates["gemini-3.8-flash-tiered"]
	if st == nil || !st.Unavailable || st.NextRetryAfter.Sub(now) != cd {
		t.Fatalf("tiered state: %+v", st)
	}
	if other := a.ModelStates["gemini-2.5-flash"]; other != nil && other.Unavailable {
		t.Fatalf("other model touched: %+v", other)
	}
	if a.Status != StatusActive {
		t.Fatalf("credential status changed: %s", a.Status)
	}
}
