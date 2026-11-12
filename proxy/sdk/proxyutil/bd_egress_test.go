package proxyutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecret = "unit-test-zone-password-NEVER-IN-PAYLOAD"

func TestRenderBDUsername(t *testing.T) {
	got := RenderBDUsername("brd-customer-ex-zone-isp", "", "198.51.100.10")
	want := "brd-customer-ex-zone-isp-ip-198.51.100.10-dns-remote-route_err-block"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := RenderBDUsername("base", "custom-user-no-placeholders", "1.2.3.4"); got != "custom-user-no-placeholders" {
		t.Fatalf("custom override = %q", got)
	}
}

func TestBDEgressID(t *testing.T) {
	if got := BDEgressID("198.51.100.10"); got != "bd/198.51.100.10" {
		t.Fatalf("got %q", got)
	}
	if got := BDEgressID("bd/198.51.100.10"); got != "bd/198.51.100.10" {
		t.Fatalf("idempotent = %q", got)
	}
	if !IsBDEgressID("bd/1.2.3.4") || IsBDEgressID("7fm6r3az/1.2.3.4") {
		t.Fatal("IsBDEgressID")
	}
}

func TestIsBDAuth(t *testing.T) {
	if !IsBDAuth(map[string]any{EgressKindKey: "bd"}, "") {
		t.Fatal("kind")
	}
	if !IsBDAuth(map[string]any{EgressIDKey: "bd/1.2.3.4"}, "") {
		t.Fatal("id in metadata")
	}
	if IsBDAuth(map[string]any{EgressIDKey: "7fm6r3az/1.2.3.4"}, "") {
		t.Fatal("self-built is not bd")
	}
	if IsBDAuth(nil, "") {
		t.Fatal("empty")
	}
}

func TestStripAndRedactAndInject(t *testing.T) {
	bare, err := BuildBDRelayURL("user-ip-1.2.3.4", "10.0.0.1:3142")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare, testSecret) || strings.Contains(bare, ":@") {
		t.Fatalf("bare url has a password: %s", bare)
	}
	if !strings.HasPrefix(bare, "http://user-ip-1.2.3.4@10.0.0.1:3142") {
		t.Fatalf("bare = %s", bare)
	}

	injected, err := InjectBDPassword(bare, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(injected, testSecret) {
		t.Fatal("inject did not add password")
	}

	stripped := StripProxyPassword(injected)
	if strings.Contains(stripped, testSecret) {
		t.Fatalf("StripProxyPassword leaked secret: %s", stripped)
	}
	redacted := RedactProxyURL(injected)
	if strings.Contains(redacted, testSecret) {
		t.Fatalf("RedactProxyURL leaked secret: %s", redacted)
	}
	if !strings.Contains(redacted, "***") {
		t.Fatalf("redacted = %s", redacted)
	}

	// A leftover stored password is discarded and replaced by the Secret.
	leftover, _ := InjectBDPassword(bare, "stale-should-be-ignored")
	replaced, err := InjectBDPassword(leftover, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(replaced, "stale-should-be-ignored") || !strings.Contains(replaced, testSecret) {
		t.Fatalf("did not override leftover: %s", RedactProxyURL(replaced))
	}

	// Error strings that embed the URL are redacted.
	errStr := RedactProxyURL(`Get "http://user:` + testSecret + `@10.0.0.1:3142": EOF`)
	if strings.Contains(errStr, testSecret) {
		t.Fatalf("error redact leaked: %s", errStr)
	}
}

func TestEffectiveProxyURL(t *testing.T) {
	bare, _ := BuildBDRelayURL("user", "10.0.0.1:3142")
	got, err := EffectiveProxyURL(bare, map[string]any{EgressKindKey: "bd"}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, testSecret) {
		t.Fatal("bd effective url missing password")
	}
	// Non-BD is unchanged, even if a password happens to be in the stored URL.
	other := "http://u:p@10.0.0.2:3141"
	got, err = EffectiveProxyURL(other, map[string]any{EgressKindKey: ""}, testSecret)
	if err != nil || got != other {
		t.Fatalf("non-bd = %q %v", got, err)
	}
	_, err = EffectiveProxyURL(bare, map[string]any{EgressKindKey: "bd"}, "")
	if err == nil {
		t.Fatal("empty password must fail closed")
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestLoadBDPasswordFromFileAndEnv(t *testing.T) {
	SetBDPasswordForTest(nil)
	t.Cleanup(func() {
		SetBDPasswordForTest(nil)
		_ = os.Unsetenv(BDPasswordEnv)
		_ = os.Unsetenv(BDPasswordFileEnv)
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "password")
	if err := os.WriteFile(path, []byte(testSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BDPasswordFileEnv, path)
	t.Setenv(BDPasswordEnv, "env-should-lose-to-file")
	if got := LoadBDPassword(); got != testSecret {
		t.Fatalf("file = %q", got)
	}
	if !HasBDPassword() {
		t.Fatal("HasBDPassword")
	}

	t.Setenv(BDPasswordFileEnv, "")
	t.Setenv(BDPasswordEnv, testSecret)
	if got := LoadBDPassword(); got != testSecret {
		t.Fatalf("env = %q", got)
	}

	// Override for tests wins.
	other := "override"
	SetBDPasswordForTest(&other)
	if got := LoadBDPassword(); got != "override" {
		t.Fatalf("override = %q", got)
	}
}

func TestAuthJSONNeverContainsPassword(t *testing.T) {
	bare, _ := BuildBDRelayURL("user-ip-198.51.100.10", "10.42.0.10:3142")
	// This is what we persist on the credential.
	auth := map[string]any{
		"proxy_url":    bare,
		"egress_id":    "bd/198.51.100.10",
		"egress_ip":    "198.51.100.10",
		"egress_kind":  "bd",
		"has_password": HasBDPassword(), // panel-style; boolean only
	}
	// Inject only at request-build time; the stored object must stay clean.
	if _, err := InjectBDPassword(bare, testSecret); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testSecret) {
		t.Fatalf("auth_json leaked secret: %s", raw)
	}
	if strings.Contains(string(raw), "user-ip-198.51.100.10:"+testSecret) {
		t.Fatal("password joined into proxy_url")
	}
	// Panel payload: hasPassword only.
	panel := map[string]any{"enabled": false, "has_password": true, "items": []any{}}
	praw, _ := json.Marshal(panel)
	if strings.Contains(string(praw), testSecret) {
		t.Fatalf("panel leaked: %s", praw)
	}
}
