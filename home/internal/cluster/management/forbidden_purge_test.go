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
