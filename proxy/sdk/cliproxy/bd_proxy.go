package cliproxy

import (
	"fmt"
	"net/http"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// effectiveAuthProxyURL returns the proxy URL used to build an HTTP client.
// For Bright Data (egress_kind=bd) credentials the zone password is injected
// from the Secret at request-build time; the stored Auth.ProxyURL stays
// password-less. A missing password fails closed (no direct fallback).
func effectiveAuthProxyURL(auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	stored := auth.ProxyURL
	if !proxyutil.IsBDAuth(auth.Metadata, "") {
		return stored, nil
	}
	return proxyutil.EffectiveProxyURL(stored, auth.Metadata, proxyutil.LoadBDPassword())
}

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if f.err == nil {
		return nil, fmt.Errorf("credential egress proxy unusable")
	}
	return nil, f.err
}
