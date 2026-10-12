package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

// Two Home replicas share one database: a flow completed on replica A must be answered
// idempotently on replica B, also well after the old 1-minute completed TTL.
func TestOAuthCallbackIdempotentAcrossReplicas(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repoA, repoB := cluster.NewRepository(db), cluster.NewRepository(db)
	homeA, homeB := NewHandler(repoA, nil, "127.0.0.1", 0), NewHandler(repoB, nil, "127.0.0.1", 0)
	engineB := gin.New()
	engineB.POST("/oauth-callback", homeB.handleOAuthCallback)
	engineB.GET("/get-auth-status", homeB.GetAuthStatus)
	_ = homeA

	ctx := context.Background()
	record, _ := cluster.NewOAuthSessionRecord("antigravity", "agv-multi", map[string]any{"redirect_uri": "x"}, time.Now().UTC())
	if err := repoA.UpsertOAuthSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	auth := &coreauth.Auth{ID: "11111111-1111-4111-8111-111111111111", Index: "11111111-1111-4111-8111-111111111111", Provider: "antigravity", Label: "a@b.c", Metadata: map[string]any{"email": "a@b.c", "refresh_token": "r1"}}
	if err := repoA.CompleteOAuthSessionWithAuthsResult(ctx, record.State, []*coreauth.Auth{auth}, map[string]any{"credential_id": auth.ID, "email": "a@b.c", "updated_existing": false}); err != nil {
		t.Fatal(err)
	}
	// A new flow started later cleans expired completed sessions; ours must survive.
	other, _ := cluster.NewOAuthSessionRecord("antigravity", "agv-other", nil, time.Now().UTC())
	if err := repoB.UpsertOAuthSession(ctx, other); err != nil {
		t.Fatal(err)
	}
	stored, _ := repoB.GetOAuthSession(ctx, record.State)
	if stored == nil || stored.ExpiresAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("completed session should be retained for hours, got %+v", stored)
	}

	body, _ := json.Marshal(map[string]string{"provider": "antigravity", "redirect_url": "http://localhost:51121/oauth-callback?state=agv-multi&code=c"})
	req := httptest.NewRequest(http.MethodPost, "/oauth-callback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engineB.ServeHTTP(rec, req)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got["status"] != "ok" || got["already_completed"] != true || got["credential_id"] != auth.ID {
		t.Fatalf("repeat callback on replica B = %d %v", rec.Code, got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/get-auth-status?state=agv-multi", nil)
	statusRec := httptest.NewRecorder()
	engineB.ServeHTTP(statusRec, statusReq)
	got = nil
	_ = json.Unmarshal(statusRec.Body.Bytes(), &got)
	if got["status"] != "ok" || got["email"] != "a@b.c" {
		t.Fatalf("status on replica B = %v", got)
	}
}

func TestOAuthLoginForExistingEmailUpdatesInsteadOfDuplicating(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	ctx := context.Background()
	existing := &coreauth.Auth{ID: "22222222-2222-4222-8222-222222222222", Index: "22222222-2222-4222-8222-222222222222", Provider: "antigravity", Label: "Dup@x.y",
		Attributes: map[string]string{"excluded_models": "chat_*"},
		Metadata:   map[string]any{"type": "antigravity", "email": "dup@x.y", "refresh_token": "old"}}
	if _, err := repo.UpsertAuth(ctx, existing, "upsert"); err != nil {
		t.Fatal(err)
	}
	record, _ := cluster.NewOAuthSessionRecord("antigravity", "agv-dup", nil, time.Now().UTC())
	_ = repo.UpsertOAuthSession(ctx, record)

	found := h.findActiveCredentialByEmail(ctx, "antigravity", "DUP@x.y")
	if found == nil || found.ID != existing.ID {
		t.Fatalf("dedupe lookup = %+v", found)
	}
	sctx := context.WithValue(ctx, oauthSessionStateKey{}, record.State)
	_ = h.refreshExistingOAuthCredential(sctx, found, map[string]any{"email": "dup@x.y", "refresh_token": "new", "access_token": "at"})

	auths, _ := repo.ListAuths(ctx)
	if len(auths) != 1 {
		t.Fatalf("auths = %d, want 1 (no duplicate)", len(auths))
	}
	if auths[0].Metadata["refresh_token"] != "new" || auths[0].Attributes["excluded_models"] != "chat_*" {
		t.Fatalf("existing credential not merged correctly: %+v %+v", auths[0].Metadata, auths[0].Attributes)
	}
	session, _ := repo.GetOAuthSession(ctx, record.State)
	if res := cluster.OAuthSessionResult(session); res["updated_existing"] != true {
		t.Fatalf("session result = %v", res)
	}
}
