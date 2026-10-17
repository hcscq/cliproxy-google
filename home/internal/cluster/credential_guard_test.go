package cluster

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestCredentialSecretsLost(t *testing.T) {
	full := `{"metadata":{"refresh_token":"r","access_token":"a","egress_id":"e"}}`
	light := `{"metadata":{"egress_id":"e","egress_ip":"1.2.3.4","proxy_url":"p","subscription_tier":"g1-pro-tier"}}`
	if got := credentialSecretsLost(full, light); got != "refresh_token" {
		t.Fatalf("light over full: got %q", got)
	}
	if got := credentialSecretsLost(full, `{"metadata":{"refresh_token":"r2","access_token":"a2"}}`); got != "" {
		t.Fatalf("rotation must be allowed: got %q", got)
	}
	if got := credentialSecretsLost(light, light); got != "" {
		t.Fatalf("no stored secrets: got %q", got)
	}
	if got := credentialSecretsLost(full, `{"attributes":{"auth_kind":"apikey","api_key":"k"},"metadata":{"type":"codex"}}`); got != "" {
		t.Fatalf("deliberate kind change must be allowed: got %q", got)
	}
	if got := credentialSecretsLost(full, `{"metadata":{"refresh_token":"r","access_token":""}}`); got != "access_token" {
		t.Fatalf("emptied access token: got %q", got)
	}
}

// Regression for the 2026-10-17 outage: a light runtime copy (egress/tier only)
// persisted over a full row wiped refresh tokens. The repository must refuse it.
func TestUpsertAuthRejectsLightCopyOverFullRow(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	id := "aaaaaaaa-2222-4333-8444-555555555555"
	full := &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive,
		Metadata: map[string]any{"refresh_token": "rt", "access_token": "at", "email": "x@example.test", "egress_id": "e1"}}
	if _, err := repo.UpsertAuth(ctx, full, "create"); err != nil {
		t.Fatal(err)
	}
	light := &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive,
		Metadata: map[string]any{"egress_id": "e1", "egress_ip": "1.2.3.4", "proxy_url": "socks5://p", "subscription_tier": "g1-pro-tier"}}
	if _, err := repo.UpsertAuth(ctx, light, "update"); !errors.Is(err, ErrAuthCredentialLoss) {
		t.Fatalf("expected ErrAuthCredentialLoss, got %v", err)
	}
	got, _, err := repo.GetAuth(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["refresh_token"] != "rt" || got.Metadata["access_token"] != "at" {
		t.Fatalf("stored secrets changed")
	}
	// A legitimate full update (token rotation) still succeeds.
	rotated := got.Clone()
	rotated.Metadata["access_token"] = "at2"
	rotated.StateVersion = 0
	if _, err := repo.UpsertAuth(ctx, rotated, "update"); err != nil {
		t.Fatalf("rotation rejected: %v", err)
	}
}
