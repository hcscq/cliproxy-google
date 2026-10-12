package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestAuthEntryModelScopedCooldownIsNotWholeCredential(t *testing.T) {
	now := time.Now()
	auth := &coreauth.Auth{ID: "x", Provider: "antigravity", Unavailable: true, NextRetryAfter: now.Add(time.Minute),
		Quota: coreauth.QuotaState{Exceeded: true, Reason: "quota"},
		ModelStates: map[string]*coreauth.ModelState{
			"gemini-3-flash": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &coreauth.Error{HTTPStatus: 429}},
		}}
	e := authFileEntry(auth)
	if e["unavailable"] != false || e["credential_scope_error"] != nil || e["tried_models_all_cooling"] != true {
		t.Fatalf("entry = %v", e)
	}
	auth.Quota.Reason = "credential_quota"
	e = authFileEntry(auth)
	if e["unavailable"] != true || e["credential_scope_error"] != true {
		t.Fatalf("credential quota entry = %v", e)
	}
}
