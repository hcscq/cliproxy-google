package management

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestOAuthCancelledWorkerCannotStoreCredentials(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	for _, cancelled := range []bool{false, true} {
		state := "success-state"
		if cancelled {
			state = "cancel-state"
		}
		session, err := cluster.NewOAuthSessionRecord("kimi-ai", state, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err = repo.UpsertOAuthSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(context.Background(), oauthSessionStateKey{}, state)
		ready, finish := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(ready)
			<-finish
			done <- h.storeOAuthMetadataWithContext(ctx, map[string]any{"type": "kimi-ai", "access_token": "fixture", "refresh_token": "fixture"}, "kimi-ai-fixture.json")
		}()
		<-ready
		if cancelled {
			if _, errCancel := cluster.NewRepository(db).CancelOAuthSession(context.Background(), state); errCancel != nil {
				t.Fatal(errCancel)
			}
		}
		close(finish)
		err = <-done
		if cancelled && !errors.Is(err, cluster.ErrOAuthSessionNotPending) {
			t.Fatalf("late credential save = %v", err)
		}
		if !cancelled && err != nil {
			t.Fatal(err)
		}
	}
	auths, err := repo.ListAuths(context.Background())
	if err != nil || len(auths) != 1 || auths[0].Provider != "kimi-ai" {
		t.Fatalf("auths after success and cancellation: count=%d err=%v", len(auths), err)
	}
}

func TestOAuthSessionContextStopsAfterRemoteCancellation(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	session, err := cluster.NewOAuthSessionRecord("codex", "cancel-acquisition", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.UpsertOAuthSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := h.oauthSessionContext(context.Background(), session.State, time.Minute)
	defer cancel()
	if _, errCancel := cluster.NewRepository(db).CancelOAuthSession(context.Background(), session.State); errCancel != nil {
		t.Fatal(errCancel)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("acquisition stopped by %v", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled acquisition kept running")
	}
}
