package home

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Subscription tier: loadCodeAssist returns paidTier {id,name} (e.g. g1-pro-tier
// "Google AI Pro", free-tier "Antigravity Starter Quota"). The leader Home refreshes it
// once a day per active credential, through the credential's pinned egress only, and
// stores it in metadata so the list can show it without extra upstream calls.

const (
	SubscriptionTierKey     = "subscription_tier"
	SubscriptionTierNameKey = "subscription_tier_name"
	subscriptionCheckedKey  = "subscription_checked_at"

	subscriptionScanEvery   = time.Minute
	subscriptionRefreshAge  = 24 * time.Hour
	subscriptionRetryAge    = time.Hour
	subscriptionPerScan     = 20
	subscriptionCallTimeout = 20 * time.Second
	subscriptionLoadURL     = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	subscriptionUserAgent   = "antigravity/1.21.9 darwin/arm64 google-api-nodejs-client/10.3.0"
)

// parseSubscriptionTier prefers paidTier, falling back to currentTier.
func parseSubscriptionTier(body []byte) (id, name string, ok bool) {
	var resp struct {
		PaidTier    *struct{ ID, Name string } `json:"paidTier"`
		CurrentTier *struct{ ID, Name string } `json:"currentTier"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return "", "", false
	}
	for _, t := range []*struct{ ID, Name string }{resp.PaidTier, resp.CurrentTier} {
		if t != nil && strings.TrimSpace(t.ID) != "" {
			return strings.TrimSpace(t.ID), strings.TrimSpace(t.Name), true
		}
	}
	return "", "", false
}

// subscriptionNeedsCheck reports whether a full (DB) auth needs an upstream refresh.
func subscriptionNeedsCheck(a *coreauth.Auth, now time.Time) bool {
	if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "antigravity") || strings.TrimSpace(a.ProxyURL) == "" {
		return false
	}
	checked := metadataTime(a, subscriptionCheckedKey)
	return checked.IsZero() || now.Sub(checked) >= subscriptionRefreshAge
}

// The in-memory auths carry neither ProxyURL nor full metadata, so the loop keeps its
// own next-due map and reads the full auth (DB) only when an entry is due.
func (r *Runtime) startSubscriptionTierRefresher(ctx context.Context) {
	go func() {
		tick := time.NewTicker(subscriptionScanEvery)
		defer tick.Stop()
		nextDue := map[string]time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if r.coreManager == nil || !r.catalogLeader() {
				continue
			}
			now := time.Now().UTC()
			live := map[string]bool{}
			calls, reads := 0, 0
			for _, a := range r.coreManager.ListLight(func(a *coreauth.Auth) bool {
				return a != nil && !a.Disabled && strings.EqualFold(a.Provider, "antigravity")
			}) {
				live[a.ID] = true
				if t, ok := nextDue[a.ID]; ok && now.Before(t) {
					continue
				}
				if calls >= subscriptionPerScan || reads >= 10*subscriptionPerScan {
					continue
				}
				reads++
				full := r.fullAuth(ctx, a.ID)
				if full == nil {
					continue
				}
				if !subscriptionNeedsCheck(full, now) {
					if checked := metadataTime(full, subscriptionCheckedKey); !checked.IsZero() {
						nextDue[a.ID] = checked.Add(subscriptionRefreshAge)
					} else {
						nextDue[a.ID] = now.Add(subscriptionRetryAge) // no egress / disabled
					}
					continue
				}
				calls++
				if r.refreshSubscriptionTier(ctx, a.ID, now) {
					nextDue[a.ID] = now.Add(subscriptionRefreshAge)
				} else {
					nextDue[a.ID] = now.Add(subscriptionRetryAge)
				}
			}
			for id := range nextDue {
				if !live[id] {
					delete(nextDue, id)
				}
			}
		}
	}()
}

func (r *Runtime) refreshSubscriptionTier(ctx context.Context, id string, now time.Time) bool {
	cctx, cancel := context.WithTimeout(ctx, subscriptionCallTimeout)
	defer cancel()
	full := r.fullAuth(cctx, id)
	if full == nil || full.Disabled || strings.TrimSpace(full.ProxyURL) == "" {
		return false
	}
	token := extractAccessToken(full)
	if token == "" {
		return false
	}
	reqBody := []byte(`{"metadata":{"ide_type":"ANTIGRAVITY","ide_version":"1.21.9","ide_name":"antigravity"}}`)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, subscriptionLoadURL, bytes.NewReader(reqBody))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", subscriptionUserAgent)
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	resp, err := credentialHTTPClient(full, subscriptionCallTimeout).Do(req)
	if err != nil {
		log.Warnf("subscription tier: %s: %v", id, err)
		return false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warnf("subscription tier: %s: http %d", id, resp.StatusCode)
		return false
	}
	tier, name, ok := parseSubscriptionTier(data)
	if !ok {
		return false
	}
	// Re-read right before writing so a concurrent token refresh is not overwritten.
	full = r.fullAuth(cctx, id)
	if full == nil {
		return false
	}
	if full.Metadata == nil {
		full.Metadata = map[string]any{}
	}
	changed := full.Metadata[SubscriptionTierKey] != tier
	full.Metadata[SubscriptionTierKey] = tier
	full.Metadata[SubscriptionTierNameKey] = name
	full.Metadata[subscriptionCheckedKey] = now.Format(time.RFC3339)
	if _, err := r.coreManager.Update(cctx, full); err != nil {
		log.WithError(err).Warnf("subscription tier: update %s failed", id)
		return false
	}
	if changed {
		log.Infof("subscription tier: %s (%s) -> %s (%s)", id, full.Label, tier, name)
	}
	return true
}
