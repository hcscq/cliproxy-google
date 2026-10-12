package home

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

const verifyBody = `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudcode-pa.googleapis.com","metadata":{"validation_url":"https://accounts.google.com/signin/continue?REDACTED"}}]}}`

func TestClassifyAccountBlock(t *testing.T) {
	if code, _ := classifyAccountBlock(403, verifyBody); code != accountBlockVerify {
		t.Fatalf("verify body = %q", code)
	}
	if code, _ := classifyAccountBlock(403, `{"error":{"code":403,"message":"Permission denied on resource project x.","status":"PERMISSION_DENIED"}}`); code != "" {
		t.Fatalf("generic/project 403 must stay model-scoped, got %q", code)
	}
	if code, _ := classifyAccountBlock(429, verifyBody); code != "" {
		t.Fatal("non-403 classified")
	}
	if got := upstreamMessage(verifyBody); got != "Verify your account to continue." {
		t.Fatalf("upstreamMessage = %q", got)
	}
}

func TestFindAndDisableAccountBlock(t *testing.T) {
	now := time.Now().UTC()
	st := &coreauth.ModelState{UpdatedAt: now, LastError: &coreauth.Error{HTTPStatus: http.StatusForbidden, Message: verifyBody}}
	auth := &coreauth.Auth{ID: "acct-403", Index: "acct-403", Provider: "antigravity", Status: coreauth.StatusActive,
		Metadata: map[string]any{}, ModelStates: map[string]*coreauth.ModelState{"gemini-2.5-flash": st}}
	f, ok := findAccountBlock(auth)
	if !ok || f.code != accountBlockVerify || f.model != "gemini-2.5-flash" {
		t.Fatalf("finding = %+v %v", f, ok)
	}
	// A 403 older than a manual/auto re-enable is ignored (no immediate re-disable).
	auth.Metadata[AccountBlockClearedKey] = now.Add(time.Minute).Format(time.RFC3339)
	if _, ok := findAccountBlock(auth); ok {
		t.Fatal("stale 403 re-triggered after re-enable")
	}
	delete(auth.Metadata, AccountBlockClearedKey)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{coreManager: manager}
	rt.disableForAccountBlock(context.Background(), auth.ID, f, now)
	got, _ := manager.GetByID(auth.ID)
	if !got.Disabled || got.Status != coreauth.StatusDisabled || got.Metadata[AccountBlockReasonKey] != accountBlockVerify {
		t.Fatalf("not disabled with reason: %+v", got)
	}
	if got.StatusMessage == "" || got.LastError == nil || got.LastError.Code != accountBlockVerify {
		t.Fatalf("missing visible reason: %q %+v", got.StatusMessage, got.LastError)
	}
	rt.liftAccountBlock(context.Background(), got, now.Add(time.Hour))
	got, _ = manager.GetByID(auth.ID)
	if got.Disabled || got.Metadata[AccountBlockReasonKey] != nil {
		t.Fatalf("not re-enabled: %+v", got)
	}
	if _, ok := findAccountBlock(got); ok {
		t.Fatal("old 403 state re-triggered after lift")
	}
}
