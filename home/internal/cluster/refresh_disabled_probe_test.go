package cluster

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func newDisabledProbeAuth(id string) *coreauth.Auth {
	a := newInvalidGrantRefreshAuth(id)
	a.Disabled = true
	a.Unavailable = true
	a.Status = coreauth.StatusDisabled
	a.StatusMessage = "disabled via management API"
	a.Metadata["note"] = "3.8首字卡顿暂停 10-20"
	return a
}

func setupDisabledProbe(t *testing.T, id string, rt *refreshTestRoundTripper) (*Repository, *RefreshController, *coreauth.Auth) {
	t.Helper()
	ctx := context.Background()
	repo := newRefreshTestRepository(t)
	auth := newDisabledProbeAuth(id)
	if _, err := repo.UpsertAuth(ctx, auth, "register"); err != nil {
		t.Fatalf("UpsertAuth: %v", err)
	}
	runtime := newRefreshTestRuntime(t, repo, auth, rt)
	coordinator := NewCoordinator(repo, NodeIdentity{IP: "127.0.0.1", Port: 9341, Secret: "master-secret"}, CoordinatorOptions{})
	markRefreshTestMaster(t, repo, coordinator)
	return repo, NewRefreshController(coordinator, runtime, repo, nil), auth
}

func requireStillDisabledWithNote(t *testing.T, a *coreauth.Auth, wantStatusMsg string) {
	t.Helper()
	if a == nil || !a.Disabled || a.Status != coreauth.StatusDisabled || !a.Unavailable {
		t.Fatalf("auth not disabled: %#v", a)
	}
	if a.StatusMessage != wantStatusMsg {
		t.Fatalf("StatusMessage = %q, want %q", a.StatusMessage, wantStatusMsg)
	}
	if got, _ := a.Metadata["note"].(string); got != "3.8首字卡顿暂停 10-20" {
		t.Fatalf("note = %q", got)
	}
	if !a.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, probe must not schedule background refresh", a.NextRefreshAfter)
	}
	if a.LastError != nil {
		t.Fatalf("LastError = %#v, probe must not change it", a.LastError)
	}
}

func TestDisabledCredentialRefreshStillRefusedWithoutProbeFlag(t *testing.T) {
	rt := &refreshTestRoundTripper{statusCode: http.StatusOK, body: `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`}
	repo, controller, _ := setupDisabledProbe(t, "ag-disabled-noflag", rt)
	if _, err := controller.RefreshNow(context.Background(), "ag-disabled-noflag"); err == nil {
		t.Fatal("refresh of disabled credential without probe flag succeeded")
	}
	if rt.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", rt.calls)
	}
	got, _, _ := repo.GetAuth(context.Background(), "ag-disabled-noflag")
	if got.Metadata["access_token"] != "stored-access-secret" {
		t.Fatal("token changed without probe flag")
	}
}

func TestDisabledCredentialProbeRefreshRotatesTokenAndStaysDisabled(t *testing.T) {
	const id = "ag-disabled-probe-ok"
	rt := &refreshTestRoundTripper{statusCode: http.StatusOK, body: `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`}
	repo, controller, _ := setupDisabledProbe(t, id, rt)
	payload, err := controller.RefreshNow(coreauth.WithDisabledProbeRefresh(context.Background()), id)
	if err != nil {
		t.Fatalf("probe refresh error = %v", err)
	}
	if len(payload) == 0 || rt.calls != 1 {
		t.Fatalf("payload len=%d calls=%d", len(payload), rt.calls)
	}
	persisted, _, _ := repo.GetAuth(context.Background(), id)
	if persisted.Metadata["access_token"] != "new-access" {
		t.Fatal("access token not rotated")
	}
	if persisted.LastRefreshedAt.IsZero() || persisted.LastRefreshError != nil {
		t.Fatalf("refresh bookkeeping: at=%v err=%#v", persisted.LastRefreshedAt, persisted.LastRefreshError)
	}
	requireStillDisabledWithNote(t, persisted, "disabled via management API")
	inMemory, ok := controller.runtime.CoreManager().GetByID(id)
	if !ok {
		t.Fatal("in-memory auth missing")
	}
	requireStillDisabledWithNote(t, inMemory, "disabled via management API")
	if controller.runtime.CoreManager().ShouldRefreshCredential(inMemory, time.Now().UTC()) {
		t.Fatal("disabled credential became eligible for background refresh")
	}
}

func TestDisabledCredentialProbeRefreshFailureOnlyRecordsError(t *testing.T) {
	const id = "ag-disabled-probe-fail"
	rt := &refreshTestRoundTripper{body: `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`}
	repo, controller, _ := setupDisabledProbe(t, id, rt)
	if _, err := controller.RefreshNow(coreauth.WithDisabledProbeRefresh(context.Background()), id); err == nil {
		t.Fatal("want refresh error")
	}
	persisted, _, _ := repo.GetAuth(context.Background(), id)
	if persisted.LastRefreshError == nil {
		t.Fatal("refresh error not recorded")
	}
	if persisted.Metadata["access_token"] != "stored-access-secret" {
		t.Fatal("token changed on failure")
	}
	requireStillDisabledWithNote(t, persisted, "disabled via management API")
}
