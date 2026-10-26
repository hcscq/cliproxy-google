package home

import (
	"context"
	"errors"
	"net/http"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Reserve AI-probe gate (储备凭据 AI 请求探测): with routing.reserve.ai-probe off
// (default) reserved credentials never get a background generateContent. Ban /
// health checks use fetchAvailableModels + loadCodeAssist, which return the same
// TOS 403 for banned accounts ("Verify your account" is not visible there: the
// list returns 200 for those, so that state is only found by real requests).

// isReservedNow reports whether id is in reserve (cached set; store fallback on miss
// so credentials just imported as reserve are covered before the next sync).
func (r *Runtime) isReservedNow(ctx context.Context, id string) bool {
	if r == nil || id == "" {
		return false
	}
	if _, ok := r.ReservedSince()[id]; ok {
		return true
	}
	s := r.reserveStore()
	if s == nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m, err := s.ListReservedCredentials(cctx)
	if err != nil {
		return false
	}
	_, ok := m[id]
	return ok
}

// reserveAIProbeBlocked reports whether a background generateContent to id must be
// skipped because it is reserved and routing.reserve.ai-probe is off.
func (r *Runtime) reserveAIProbeBlocked(ctx context.Context, id string) bool {
	if r == nil || r.reserveSettings().AIProbe {
		return false
	}
	return r.isReservedNow(ctx, id)
}

// isTOSBan reports a terms-of-service / suspended account 403 (not "verify your account").
func isTOSBan(status int, body string) (string, string, bool) {
	c, label := classifyAccountBlock(status, body)
	if c == accountBlockSuspended {
		return c, label, true
	}
	return "", "", false
}

// disableForListBan disables id after a TOS 403 from fetchAvailableModels or
// loadCodeAssist, with the same status / reason as a generate-403 ban.
func (r *Runtime) disableForListBan(ctx context.Context, id, source string, status int, body string, now time.Time) bool {
	c, label, ok := isTOSBan(status, body)
	if !ok {
		return false
	}
	f := accountBlockFinding{code: c, label: label, upstream: upstreamMessage(body), model: source, at: now}
	r.disableForAccountBlock(ctx, id, f, now)
	log.Warnf("account block guard: %s %s -> 403 %s (disabled)", source, id, label)
	return true
}

// listBanCheck checks one credential without an AI request: model list (also stored as
// its catalog) and loadCodeAssist. Returns status of the failing call (or 200) and
// whether a TOS ban was found (and the credential disabled).
func (r *Runtime) listBanCheck(ctx context.Context, full *coreauth.Auth, now time.Time) (int, bool, string) {
	if full == nil {
		return 0, false, "not found"
	}
	_, err := r.refreshAntigravityCatalogNow(ctx, full.ID, "ban-check")
	var he *antigravityHTTPError
	if errors.As(err, &he) {
		if _, label, ok := isTOSBan(he.status, he.body); ok {
			return he.status, true, label // refreshAntigravityCatalogNow already disabled it
		}
		if he.status != http.StatusOK {
			return he.status, false, http.StatusText(he.status) + ": " + upstreamMessage(he.body)
		}
	}
	var hp HealthProbeResult
	r.healthLoadCodeAssist(ctx, full, &hp)
	if hp.TierStatus == http.StatusForbidden {
		if r.disableForListBan(ctx, full.ID, "loadCodeAssist", hp.TierStatus, hp.TierError, now) {
			_, label, _ := isTOSBan(hp.TierStatus, hp.TierError)
			return hp.TierStatus, true, label
		}
	}
	if err != nil && !errors.Is(err, errCatalogInFlight) {
		return 0, false, clip(err.Error(), 200)
	}
	if hp.TierStatus != 0 && hp.TierStatus != http.StatusOK {
		return hp.TierStatus, false, "loadCodeAssist " + http.StatusText(hp.TierStatus)
	}
	return http.StatusOK, false, ""
}
