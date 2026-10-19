package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestClassifyForbidden(t *testing.T) {
	cases := map[string]string{
		"This service has been disabled in this account for violation of Terms of Service.": forbiddenTOS,
		"Verify your account to continue.":                                                  forbiddenVerify,
		"The caller does not have permission":                                               forbiddenPermission,
		"something odd":                                                                     forbiddenOther,
	}
	for m, want := range cases {
		if got := classifyForbidden(m); got != want {
			t.Errorf("%q: got %s want %s", m, got, want)
		}
	}
}

func TestForbiddenEvidence(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{ID: "a", Disabled: true, Metadata: map[string]any{"type": "antigravity", "auto_disabled_reason": "account_suspended", "auto_disabled_at": now.Format(time.RFC3339)},
		LastError: &coreauth.Error{HTTPStatus: 403, Message: "violation of Terms of Service"}}
	cr, ok := forbiddenEvidence(a, nil)
	if !ok || cr.Category != forbiddenTOS || cr.Source != "auto_disable" {
		t.Fatalf("got %+v", cr)
	}
	if _, ok := forbiddenEvidence(&coreauth.Auth{ID: "b"}, nil); ok {
		t.Fatal("no evidence expected")
	}
	cr, ok = forbiddenEvidence(&coreauth.Auth{ID: "c"}, &cluster.AntigravityCatalogRecord{HTTPStatus: 403, Error: "x", AttemptedAt: now})
	if !ok || cr.Category != forbiddenCatalog {
		t.Fatalf("catalog: %+v", cr)
	}
}

func TestUnauthorizedEvidence(t *testing.T) {
	// Real prod shape (6c704b00): disabled, status_message unauthorized, 401 authentication_error.
	a := &coreauth.Auth{ID: "u", Disabled: true, StatusMessage: "unauthorized",
		LastError: &coreauth.Error{Code: "authentication_error", Message: "credential unauthorized", HTTPStatus: 401}}
	cr, ok := unauthorizedEvidence(a)
	if !ok || cr.Category != invalidUnauthorized || cr.Kind != "401" || !cr.Persistent {
		t.Fatalf("401: %+v ok=%v", cr, ok)
	}
	rt := &coreauth.Auth{ID: "r", Disabled: true, StatusMessage: "unauthorized",
		LastError: &coreauth.Error{Code: "invalid_grant", Message: "Token has been expired or revoked.", HTTPStatus: 400}}
	if cr, ok := unauthorizedEvidence(rt); !ok || cr.Category != invalidRefreshToken {
		t.Fatalf("refresh token: %+v ok=%v", cr, ok)
	}
	// Temporary / transient states never qualify.
	for name, x := range map[string]*coreauth.Auth{
		"enabled stray 401": {ID: "e", LastError: &coreauth.Error{HTTPStatus: 401}},
		"disabled by hand":  {ID: "d", Disabled: true},
		"quota 429":         {ID: "q", Disabled: true, LastError: &coreauth.Error{HTTPStatus: 429, Code: "quota_bucket_exhausted"}},
		"cooling":           {ID: "c", Unavailable: true, StatusMessage: "quota exhausted"},
	} {
		if _, ok := unauthorizedEvidence(x); ok {
			t.Errorf("%s must not qualify", name)
		}
	}
}

func TestInvalidCategoryPrecedence(t *testing.T) {
	// A TOS ban that is also flagged unauthorized stays in the 403 TOS category
	// (pre-existing classification unchanged; it is only listed once).
	a := &coreauth.Auth{ID: "a", Disabled: true, StatusMessage: "unauthorized", Metadata: map[string]any{"auto_disabled_reason": "account_suspended"},
		LastError: &coreauth.Error{HTTPStatus: 403, Message: "violation of Terms of Service"}}
	if cr, ok := forbiddenEvidence(a, nil); !ok || cr.Category != forbiddenTOS {
		t.Fatalf("got %+v", cr)
	}
}
