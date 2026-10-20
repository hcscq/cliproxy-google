// Package agversion resolves the latest Antigravity client version (same hub
// manifest the proxy follows) and caches it; callers never fetch per request.
package agversion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	// Fallback mirrors the previously hardcoded catalog/quota version.
	Fallback        = "1.11.5"
	refreshInterval = 3 * time.Hour
	fetchTimeout    = 10 * time.Second
)

var manifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"

var (
	mu        sync.RWMutex
	cached    string
	startOnce sync.Once
)

// Start launches the background refresher (idempotent).
func Start(ctx context.Context) {
	startOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		go func() {
			refresh(ctx)
			t := time.NewTicker(refreshInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					refresh(ctx)
				}
			}
		}()
	})
}

// Version returns the cached latest version, or Fallback if not resolved yet.
func Version() string {
	Start(context.Background())
	mu.RLock()
	defer mu.RUnlock()
	if cached != "" {
		return cached
	}
	return Fallback
}

// UserAgent returns "antigravity/hub/<latest> darwin/arm64", the Antigravity Hub
// client UA the proxy also sends. Google gates some catalog entries on the hub
// form (e.g. gemini-4-argon-* for Ultra only appears with it).
func UserAgent() string { return "antigravity/hub/" + Version() + " darwin/arm64" }

// set is for tests.
func set(v string) { mu.Lock(); cached = v; mu.Unlock() }

func refresh(ctx context.Context) {
	v, err := fetch(ctx)
	if err != nil {
		log.WithError(err).Warn("antigravity version refresh failed; keeping cached/fallback")
		return
	}
	mu.Lock()
	changed := cached != v
	cached = v
	mu.Unlock()
	if changed {
		log.WithField("version", v).Info("antigravity version resolved")
	}
}

// RefreshNow fetches synchronously (used at startup before catalog warm).
func RefreshNow(ctx context.Context) { refresh(ctx) }

func fetch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "electron-builder")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	var m struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	v := strings.TrimSpace(m.Version)
	if !validSemver(v) {
		return "", errors.New("invalid manifest version " + fmt.Sprintf("%q", v))
	}
	return v, nil
}

func validSemver(v string) bool {
	p := strings.Split(v, ".")
	if len(p) != 3 {
		return false
	}
	for _, s := range p {
		if s == "" {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
