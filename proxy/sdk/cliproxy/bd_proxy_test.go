package cliproxy

import (
	"encoding/json"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

const bdProxyTestSecret = "proxy-unit-test-password-MUST-NOT-LEAK"

func TestEffectiveAuthProxyURLInjectsOnlyForBD(t *testing.T) {
	secret := bdProxyTestSecret
	proxyutil.SetBDPasswordForTest(&secret)
	t.Cleanup(func() { proxyutil.SetBDPasswordForTest(nil) })

	bare, err := proxyutil.BuildBDRelayURL("user-ip-198.51.100.10", "10.42.0.10:3142")
	if err != nil {
		t.Fatal(err)
	}
	bd := &coreauth.Auth{ProxyURL: bare, Metadata: map[string]any{"egress_kind": "bd", "egress_id": "bd/198.51.100.10"}}
	got, err := effectiveAuthProxyURL(bd)
	if err != nil || !strings.Contains(got, bdProxyTestSecret) {
		t.Fatalf("bd inject: %q %v", got, err)
	}
	raw, _ := json.Marshal(bd)
	if strings.Contains(string(raw), bdProxyTestSecret) {
		t.Fatalf("auth still password-less after inject? leaked in json: %s", raw)
	}

	self := &coreauth.Auth{ProxyURL: "http://1.2.3.4@10.0.0.1:3141", Metadata: map[string]any{"egress_id": "n/1.2.3.4"}}
	got, err = effectiveAuthProxyURL(self)
	if err != nil || got != self.ProxyURL || strings.Contains(got, bdProxyTestSecret) {
		t.Fatalf("self-built: %q %v", got, err)
	}
}

func TestRoundTripperForBDWithoutPasswordFailsClosed(t *testing.T) {
	empty := ""
	proxyutil.SetBDPasswordForTest(&empty)
	t.Cleanup(func() { proxyutil.SetBDPasswordForTest(nil) })

	bare, _ := proxyutil.BuildBDRelayURL("user", "10.42.0.10:3142")
	auth := &coreauth.Auth{ProxyURL: bare, Metadata: map[string]any{"egress_kind": "bd"}}
	rt := newDefaultRoundTripperProvider().RoundTripperFor(auth)
	if rt == nil {
		t.Fatal("nil RoundTripper can be treated as direct; want a failing one")
	}
	if _, ok := rt.(failingRoundTripper); !ok {
		t.Fatalf("type %T, want failingRoundTripper", rt)
	}
}
