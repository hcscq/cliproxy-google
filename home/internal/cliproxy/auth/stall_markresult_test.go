package auth

import (
	"context"
	"testing"
	"time"
)

func TestStallUsageResultCoolsModel(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "c1", Index: "c1", Provider: "antigravity", Status: StatusActive}
	if _, err := m.Register(WithSkipPersist(context.Background()), a); err != nil {
		t.Fatal(err)
	}
	body := `{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"3.8 慢: no first byte from upstream within 15s","cpa_cooldown_ms":45000}}`
	r := NewUsageResult("c1", "antigravity", "gemini-3.8-flash-tiered", 504, body)
	cd := 45 * time.Second
	r.RetryAfter = &cd
	m.MarkResult(context.Background(), r)
	got, _ := m.GetByID("c1")
	st := got.ModelStates["gemini-3.8-flash-tiered"]
	t.Logf("authID=%q state=%+v", m.resultAuthID(r), st)
	if blocked, _, _ := isAuthBlockedForModel(got, "gemini-3.8-flash-tiered", time.Now()); !blocked {
		t.Fatal("stalled model not blocked")
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "gemini-2.5-flash", time.Now()); blocked {
		t.Fatal("other model blocked")
	}
}
