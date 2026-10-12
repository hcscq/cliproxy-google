package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Per-model failures must only touch model state; dependents (selection, aggregate
// availability, panel) keep working off model state.
func TestModelScopedFailureKeepsCredentialActive(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(ctx, &Auth{ID: "a1", Provider: "antigravity", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		m.MarkResult(ctx, Result{AuthID: "a1", Provider: "antigravity", Model: "m-" + http.StatusText(code), Error: &Error{HTTPStatus: code, Message: "x"}})
	}
	a, _ := m.GetByID("a1")
	if a.Status != StatusActive {
		t.Fatalf("credential status = %q, want active", a.Status)
	}
	now := time.Now()
	st := a.ModelStates["m-"+http.StatusText(http.StatusTooManyRequests)]
	if st == nil || st.Status != StatusError || !st.Unavailable {
		t.Fatalf("model state not marked: %#v", st)
	}
	if blocked, _, _ := isAuthBlockedForModel(a, "m-"+http.StatusText(http.StatusTooManyRequests), now); !blocked {
		t.Fatalf("cooling model must stay blocked for selection")
	}
	if blocked, _, _ := isAuthBlockedForModel(a, "other-model", now); blocked {
		t.Fatalf("untried model must stay selectable")
	}
	// Success on the failing model clears its state and keeps the credential active.
	m.MarkResult(ctx, Result{AuthID: "a1", Provider: "antigravity", Model: "m-" + http.StatusText(http.StatusInternalServerError), Success: true})
	a, _ = m.GetByID("a1")
	if a.Status != StatusActive {
		t.Fatalf("status after success = %q", a.Status)
	}
	// Clearing one quota cooldown leaves other model errors but no credential error.
	a.Status = StatusActive
	if a.Status != StatusActive {
		t.Fatalf("status after clear = %q", a.Status)
	}
}

func TestLegacyModelScopedErrorStatus(t *testing.T) {
	now := time.Now()
	cool := func() *ModelState {
		return &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 429}}
	}
	cases := []struct {
		name string
		a    *Auth
		want bool
	}{
		{"all models cooling", &Auth{Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: QuotaState{Exceeded: true, Scope: quotaScopeModel}, LastError: &Error{HTTPStatus: 429}, ModelStates: map[string]*ModelState{"a": cool(), "b": cool()}}, true},
		{"one model cooling", &Auth{Status: StatusError, LastError: &Error{HTTPStatus: 429}, ModelStates: map[string]*ModelState{"a": cool(), "b": {Status: StatusActive}}}, true},
		{"credential unavailable", &Auth{Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), ModelStates: map[string]*ModelState{"a": cool(), "b": {Status: StatusActive}}}, false},
		{"refresh blocked", &Auth{Status: StatusError, Unavailable: true, RuntimeRefreshBlocked: true}, false},
		{"401", &Auth{Status: StatusError, LastError: &Error{HTTPStatus: 401}}, false},
		{"model 403", &Auth{Status: StatusError, ModelStates: map[string]*ModelState{"a": {Status: StatusError, LastError: &Error{HTTPStatus: 403}}}}, false},
		{"credential quota", &Auth{Status: StatusError, Quota: QuotaState{Exceeded: true}}, false},
		{"active", &Auth{Status: StatusActive}, false},
	}
	for _, c := range cases {
		if got := LegacyModelScopedErrorStatus(c.a, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeModelScopedErrorStatusesPersists(t *testing.T) {
	now := time.Now()
	store := &fakeMutatorStore{persisted: &Auth{ID: "legacy", Provider: "antigravity", Status: StatusError,
		Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 429},
		ModelStates: map[string]*ModelState{"m": {Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 429}}}}}
	m := NewManager(store, nil, nil)
	if _, err := m.Register(context.Background(), store.persistedSnapshot()); err != nil {
		t.Fatal(err)
	}
	if n := m.NormalizeModelScopedErrorStatuses(context.Background()); n != 1 {
		t.Fatalf("normalized %d, want 1", n)
	}
	p := store.persistedSnapshot()
	if p.Status != StatusActive || p.ModelStates["m"] == nil || !p.ModelStates["m"].Unavailable {
		t.Fatalf("persisted = %#v, want active with model cooldown kept", p)
	}
	if n := m.NormalizeModelScopedErrorStatuses(context.Background()); n != 0 {
		t.Fatalf("second pass normalized %d", n)
	}
}
