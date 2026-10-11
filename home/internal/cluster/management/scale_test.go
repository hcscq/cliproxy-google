package management

import (
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestParseGroupCSV(t *testing.T) {
	doc, err := ParseGroupCSV(strings.NewReader("type,group,member,label,credential_groups\ncredential,g1,a@x.com,,\ncredential,g1,id-2,,\nmodel,m1,gemini-3-flash,,g1;g2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.CredentialGroups) != 1 || len(doc.CredentialGroups[0].Members) != 2 || len(doc.ModelGroups[0].Models[0].CredentialGroups) != 2 {
		t.Fatalf("%+v", doc)
	}
	if _, err := ParseGroupCSV(strings.NewReader("bogus,g,m\n")); err == nil {
		t.Fatal("unknown type must fail")
	}
}

func TestCredentialStatusIgnoresStaleModelScopedError(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{Status: coreauth.StatusError, ModelStates: map[string]*coreauth.ModelState{
		"gemini-2.5-pro": {Status: coreauth.StatusError, Quota: coreauth.QuotaState{Exceeded: true}, NextRetryAfter: now.Add(-time.Hour)},
	}}
	if got := credentialStatus(a, now); got != credStatusOK {
		t.Fatalf("stale model error: got %s want ok", got)
	}
	a.ModelStates["gemini-3.1-flash-image"] = &coreauth.ModelState{Status: coreauth.StatusError, Unavailable: true, Quota: coreauth.QuotaState{Exceeded: true}, NextRetryAfter: now.Add(time.Hour)}
	if got := credentialStatus(a, now); got != credStatusCooling {
		t.Fatalf("active model cooldown: got %s want cooling", got)
	}
	a.Disabled = true
	if got := credentialStatus(a, now); got != credStatusDisabled {
		t.Fatalf("disabled: got %s", got)
	}
}
