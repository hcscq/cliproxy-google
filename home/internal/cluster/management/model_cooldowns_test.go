package management

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestAuthFileEntryModelScopedCooldown(t *testing.T) {
	now := time.Now()
	auth := &coreauth.Auth{
		ID: "a", Provider: "antigravity", Status: coreauth.StatusError, StatusMessage: "quota exhausted",
		ModelStates: map[string]*coreauth.ModelState{
			"gemini-2.5-pro": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour),
				StatusMessage: "quota", Quota: coreauth.QuotaState{Exceeded: true}, LastError: &coreauth.Error{HTTPStatus: 429}},
			"gemini-3-flash": {Status: coreauth.StatusActive},
			"gemini-old-503": {Status: coreauth.StatusError, StatusMessage: "no capacity", LastError: &coreauth.Error{HTTPStatus: 503}},
		},
	}
	e := authFileEntry(auth)
	if e["credential_scope_error"].(bool) {
		t.Fatal("model-scoped quota must not mark whole credential")
	}
	cd := e["model_cooldowns"].([]gin.H)
	if len(cd) != 2 || cd[0]["model"] != "gemini-2.5-pro" || cd[0]["cooling"] != true || cd[0]["http_status"] != 429 {
		t.Fatalf("unexpected cooldowns: %#v", cd)
	}
	if cd[1]["cooling"] != false {
		t.Fatalf("capacity error without deadline must not be cooling: %#v", cd[1])
	}
	auth.Unavailable = true
	auth.NextRetryAfter = now.Add(time.Minute)
	if !authFileEntry(auth)["credential_scope_error"].(bool) {
		t.Fatal("auth-level cooldown must mark whole credential")
	}
}
