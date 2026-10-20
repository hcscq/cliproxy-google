package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestAPICallTokenExpiring(t *testing.T) {
	now := time.Now()
	fresh := &coreauth.Auth{Metadata: map[string]any{"expired": now.Add(time.Hour).Format(time.RFC3339)}}
	stale := &coreauth.Auth{Metadata: map[string]any{"expired": now.Add(-time.Minute).Format(time.RFC3339)}}
	if apiCallTokenExpiring(fresh, now) {
		t.Fatal("fresh token reported expiring")
	}
	if !apiCallTokenExpiring(stale, now) {
		t.Fatal("expired token not reported expiring")
	}
	if apiCallTokenExpiring(&coreauth.Auth{}, now) {
		t.Fatal("unknown expiry reported expiring")
	}
}
