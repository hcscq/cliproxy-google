package home

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestPruneCatalogStateForgetsDisabledCredentialCatalog(t *testing.T) {
	antigravityCatalogMu.Lock()
	antigravityCatalog["live-1"] = &antigravityCatalogEntry{}
	antigravityCatalog["disabled-1"] = &antigravityCatalogEntry{}
	antigravityCatalogMu.Unlock()
	t.Cleanup(func() {
		antigravityCatalogMu.Lock()
		delete(antigravityCatalog, "live-1")
		delete(antigravityCatalog, "disabled-1")
		antigravityCatalogMu.Unlock()
	})
	r := &Runtime{}
	r.pruneCatalogState([]*coreauth.Auth{{ID: "live-1"}})
	antigravityCatalogMu.Lock()
	defer antigravityCatalogMu.Unlock()
	if _, ok := antigravityCatalog["live-1"]; !ok {
		t.Fatal("live credential catalog was dropped")
	}
	if _, ok := antigravityCatalog["disabled-1"]; ok {
		t.Fatal("disabled credential's stale catalog was kept")
	}
}
