package cliproxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string.
type defaultRoundTripperProvider struct {
	mu    sync.RWMutex
	cache map[string]http.RoundTripper
}

func newDefaultRoundTripperProvider() *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{cache: make(map[string]http.RoundTripper)}
}

// RoundTripperFor implements coreauth.RoundTripperProvider.
func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxyStr := strings.TrimSpace(auth.ProxyURL)
	if proxyStr == "" {
		return nil
	}
	// Cache by the stored (password-less) URL so the Secret never becomes a map key.
	cacheKey := proxyStr
	p.mu.RLock()
	rt := p.cache[cacheKey]
	p.mu.RUnlock()
	if rt != nil {
		return rt
	}
	buildURL, errEff := effectiveAuthProxyURL(auth)
	if errEff != nil {
		// BD with no Secret: fail closed, never go direct.
		rt = failingRoundTripper{err: errEff}
		p.mu.Lock()
		p.cache[cacheKey] = rt
		p.mu.Unlock()
		return rt
	}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(buildURL)
	if errBuild != nil {
		log.Errorf("%v", proxyutil.RedactProxyURL(errBuild.Error()))
		if proxyutil.IsBDAuth(auth.Metadata, "") {
			rt = failingRoundTripper{err: errBuild}
			p.mu.Lock()
			p.cache[cacheKey] = rt
			p.mu.Unlock()
			return rt
		}
		return nil
	}
	if transport == nil {
		if proxyutil.IsBDAuth(auth.Metadata, "") {
			return failingRoundTripper{err: fmt.Errorf("credential egress proxy unusable")}
		}
		return nil
	}
	p.mu.Lock()
	p.cache[cacheKey] = transport
	p.mu.Unlock()
	return transport
}
