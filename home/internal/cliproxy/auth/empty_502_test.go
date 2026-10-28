package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsEmptyResponse502MatchesFilterCases(t *testing.T) {
	cases := []struct {
		name string
		st   *ModelState
		want bool
	}{
		{name: "nil", want: false},
		{name: "bad gateway text", st: &ModelState{LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "Bad Gateway"}}, want: true},
		{name: "empty body via upstream", st: &ModelState{LastError: &Error{Upstream: &UpstreamResponse{Status: http.StatusBadGateway}}}, want: true},
		{name: "proxy empty stream", st: &ModelState{LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream returned an empty response (no output)"}}, want: true},
		{name: "status message only", st: &ModelState{StatusMessage: "upstream returned an empty response (no output)"}, want: true},
		{name: "real 502", st: &ModelState{LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "gfe 502"}}, want: false},
		{name: "500", st: &ModelState{LastError: &Error{HTTPStatus: http.StatusInternalServerError, Message: "Bad Gateway"}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEmptyResponse502(tc.st); got != tc.want {
				t.Fatalf("IsEmptyResponse502() = %v, want %v", got, tc.want)
			}
		})
	}
}

func empty502Mark(authID, model, message string) Result {
	return Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: message},
	}
}

func TestEmpty502StreakCoolsOneModelAndSwitches(t *testing.T) {
	defer SetEmpty502Policy(defaultEmpty502Streak, defaultEmpty502Backoff)
	SetEmpty502Policy(3, 90*time.Second)

	const model = "gemini-4-argon-medium"
	const other = "gemini-2.5-flash"
	badID, goodID := "empty502-bad", "empty502-good"
	var entered atomic.Int32
	var cleared atomic.Int32
	SetEmpty502CoolNotify(func(authID, gotModel string, until time.Time) {
		if authID != badID || gotModel != model || !until.After(time.Now()) {
			t.Errorf("enter notify = %s %s %v", authID, gotModel, until)
		}
		entered.Add(1)
	}, func(string, string) {
		cleared.Add(1)
	})
	t.Cleanup(func() {
		SetEmpty502CoolNotify(nil, nil)
		ClearEmpty502Block(badID, model)
		ClearQuotaBlock(badID, model)
		ClearQuotaBlock(goodID, model)
	})

	manager := NewManager(nil, &orderedCandidateSelector{order: []string{badID, goodID}}, nil)
	bad := &Auth{ID: badID, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"request_retry": 0}}
	good := &Auth{ID: goodID, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"request_retry": 0}}
	registerDispatchTestAuth(t, manager, bad, model, other)
	registerDispatchTestAuth(t, manager, good, model)

	for i := 1; i <= 2; i++ {
		manager.MarkResult(context.Background(), empty502Mark(badID, model, "Bad Gateway"))
		got, _ := manager.GetByID(badID)
		st := got.ModelStates[model]
		if st == nil || st.Unavailable || !st.NextRetryAfter.IsZero() {
			t.Fatalf("after %d empty 502s state = %#v, want recorded but not cooling", i, st)
		}
		if blocked, _, _ := isAuthBlockedForModel(got, model, time.Now()); blocked {
			t.Fatalf("after %d empty 502s the model was blocked", i)
		}
		decision, errDispatch := manager.Dispatch(context.Background(), []string{"codex"}, model, Options{})
		if errDispatch != nil || decision == nil || decision.Auth == nil || decision.Auth.ID != badID {
			t.Fatalf("dispatch before threshold = %#v %v, want %s", decision, errDispatch, badID)
		}
	}
	if entered.Load() != 0 {
		t.Fatalf("enter notify = %d, want 0 before threshold", entered.Load())
	}

	manager.MarkResult(context.Background(), empty502Mark(badID, model, "Bad Gateway"))
	got, _ := manager.GetByID(badID)
	st := got.ModelStates[model]
	if st == nil || !st.Unavailable || st.NextRetryAfter.Sub(time.Now()) < 80*time.Second || st.NextRetryAfter.Sub(time.Now()) > 95*time.Second {
		t.Fatalf("cooled state = %#v, want ~90s next_retry_after", st)
	}
	if got.Status != StatusActive || got.Unavailable {
		t.Fatalf("credential cooled as a whole: status=%s unavailable=%v", got.Status, got.Unavailable)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, model, time.Now()); !blocked {
		t.Fatal("cooled model was still selectable")
	}
	if blocked, _, _ := isAuthBlockedForModel(got, other, time.Now()); blocked {
		t.Fatal("sibling model was cooled")
	}
	if _, ok := QuotaBlockedUntil(badID, model); !ok {
		t.Fatal("fast block missing")
	}
	if entered.Load() != 1 {
		t.Fatalf("enter notify = %d, want 1", entered.Load())
	}
	decision, errDispatch := manager.Dispatch(context.Background(), []string{"codex"}, model, Options{})
	if errDispatch != nil || decision == nil || decision.Auth == nil || decision.Auth.ID != goodID {
		t.Fatalf("dispatch after threshold = %#v %v, want %s", decision, errDispatch, goodID)
	}
	otherDecision, errOther := manager.Dispatch(context.Background(), []string{"codex"}, other, Options{})
	if errOther != nil || otherDecision == nil || otherDecision.Auth == nil || otherDecision.Auth.ID != badID {
		t.Fatalf("sibling dispatch = %#v %v, want %s", otherDecision, errOther, badID)
	}

	// In-flight repeats must not extend the window or take another persist/notify.
	deadline := st.NextRetryAfter
	manager.MarkResult(context.Background(), empty502Mark(badID, model, "Bad Gateway"))
	got, _ = manager.GetByID(badID)
	if !got.ModelStates[model].NextRetryAfter.Equal(deadline) {
		t.Fatalf("window moved from %v to %v", deadline, got.ModelStates[model].NextRetryAfter)
	}
	if entered.Load() != 1 {
		t.Fatalf("enter notify = %d, want 1", entered.Load())
	}

	manager.MarkResult(context.Background(), Result{AuthID: badID, Provider: "codex", Model: model, Success: true})
	got, _ = manager.GetByID(badID)
	if st := got.ModelStates[model]; st != nil && (st.Unavailable || !st.NextRetryAfter.IsZero() || st.Status == StatusError) {
		t.Fatalf("success left cooling state %#v", st)
	}
	if _, ok := QuotaBlockedUntil(badID, model); ok {
		t.Fatal("fast block survived success")
	}
	if cleared.Load() != 1 {
		t.Fatalf("clear notify = %d, want 1", cleared.Load())
	}

	// Streak restarted, so one more empty 502 is not enough.
	manager.MarkResult(context.Background(), empty502Mark(badID, model, "Bad Gateway"))
	got, _ = manager.GetByID(badID)
	if blocked, _, _ := isAuthBlockedForModel(got, model, time.Now()); blocked {
		t.Fatal("streak did not reset on success")
	}
}

func TestEmpty502RealBodyStillUsesTransientCooldown(t *testing.T) {
	defer SetEmpty502Policy(defaultEmpty502Streak, defaultEmpty502Backoff)
	SetEmpty502Policy(3, 90*time.Second)
	manager := NewManager(nil, nil, nil)
	const id, model = "empty502-real", "gemini-4-argon-medium"
	if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(context.Background(), empty502Mark(id, model, "gfe 502"))
	got, _ := manager.GetByID(id)
	st := got.ModelStates[model]
	delay := st.NextRetryAfter.Sub(time.Now())
	if st == nil || !st.Unavailable || delay < 50*time.Second || delay > 70*time.Second {
		t.Fatalf("real 502 state = %#v, want ~1m cooldown", st)
	}
}

func TestEmpty502StreakPersistsOnlyAtThreshold(t *testing.T) {
	defer SetEmpty502Policy(defaultEmpty502Streak, defaultEmpty502Backoff)
	SetEmpty502Policy(3, 45*time.Second)
	const authID = "empty502-persist"
	const model = "gemini-4-argon-medium"
	store := &fakeMutatorStore{persisted: &Auth{
		ID: authID, Index: authID, Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"email": "user@example.com"},
	}}
	manager := newHomeNodeManager(t, store, authID)
	t.Cleanup(func() { ClearEmpty502Block(authID, model) })

	for i := 0; i < 2; i++ {
		manager.MarkResult(context.Background(), empty502Mark(authID, model, http.StatusText(http.StatusBadGateway)))
	}
	if store.mutationCount() != 0 {
		t.Fatalf("mutations before threshold = %d, want 0", store.mutationCount())
	}
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex", Model: model,
		Error: &Error{Upstream: &UpstreamResponse{Status: http.StatusBadGateway}},
	})
	if store.mutationCount() != 1 {
		t.Fatalf("mutations at threshold = %d, want 1", store.mutationCount())
	}
	persisted := store.persistedSnapshot().ModelStates[model]
	if persisted == nil || !persisted.Unavailable || !persisted.NextRetryAfter.After(time.Now().Add(30*time.Second)) {
		t.Fatalf("persisted state = %#v, want per-model next_retry_after", persisted)
	}
	if snap := store.persistedSnapshot(); snap.Unavailable || !snap.NextRetryAfter.IsZero() {
		t.Fatalf("credential-level cooldown set: unavailable=%v next=%v", snap.Unavailable, snap.NextRetryAfter)
	}
	local, _ := manager.GetByID(authID)
	if blocked, _, _ := isAuthBlockedForModel(local, model, time.Now()); !blocked {
		t.Fatal("local scheduler did not adopt the persisted backoff")
	}

	// Already cooling: another empty 502 must not take the row lock again.
	manager.MarkResult(context.Background(), empty502Mark(authID, model, "Bad Gateway"))
	if store.mutationCount() != 1 {
		t.Fatalf("mutations while cooling = %d, want 1", store.mutationCount())
	}
}

func TestEmpty502DisabledPolicyKeepsGenericCooldown(t *testing.T) {
	defer SetEmpty502Policy(defaultEmpty502Streak, defaultEmpty502Backoff)
	SetEmpty502Policy(-1, 0)
	if Empty502CoolingEnabled() {
		t.Fatal("negative streak still enabled")
	}
	manager := NewManager(nil, nil, nil)
	const id, model = "empty502-off", "gemini-4-argon-medium"
	if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(context.Background(), empty502Mark(id, model, "Bad Gateway"))
	got, _ := manager.GetByID(id)
	st := got.ModelStates[model]
	delay := time.Duration(0)
	if st != nil {
		delay = st.NextRetryAfter.Sub(time.Now())
	}
	if st == nil || !st.Unavailable || delay < 50*time.Second || delay > 70*time.Second {
		t.Fatalf("disabled policy state = %#v, want generic ~1m cooldown", st)
	}
}
