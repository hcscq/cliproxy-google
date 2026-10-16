package home

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/agversion"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Subscription tier: loadCodeAssist returns paidTier {id,name} (e.g. g1-pro-tier
// "Google AI Pro", free-tier "Antigravity Starter Quota"). The leader Home refreshes it
// every subscription-interval-minutes (default 1h, jittered) per active credential,
// promptly on events (RequestSubscriptionCheck),, through the credential's pinned egress only, and
// stores it in metadata so the list can show it without extra upstream calls.

const (
	SubscriptionTierKey     = "subscription_tier"
	SubscriptionTierNameKey = "subscription_tier_name"
	subscriptionCheckedKey  = "subscription_checked_at"

	subscriptionScanEvery       = time.Minute
	subscriptionDefaultInterval = time.Hour
	subscriptionDefaultPerMin   = 300
	subscriptionEventDebounce   = 2 * time.Minute
	subscriptionCallTimeout     = 20 * time.Second
	subscriptionRequestKVPrefix = "subtier:req:"
	subscriptionRequestTTL      = time.Hour
	subscriptionKVBatch         = 500
	subscriptionLoadURL         = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
)

// subscriptionSettings returns (interval, per-minute cap) from the live config.
func (r *Runtime) subscriptionSettings() (time.Duration, int) {
	interval, perMin := subscriptionDefaultInterval, subscriptionDefaultPerMin
	if r != nil {
		if cfg := r.Config(); cfg != nil {
			h := cfg.Antigravity.Health
			if h.SubscriptionIntervalMinutes > 0 {
				interval = time.Duration(h.SubscriptionIntervalMinutes) * time.Minute
			}
			if h.SubscriptionPerMinute > 0 {
				perMin = h.SubscriptionPerMinute
			}
		}
	}
	return interval, perMin
}

// subscriptionRetry is the wait after a failed check.
func subscriptionRetry(interval time.Duration) time.Duration {
	d := interval / 2
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// subscriptionJitter spreads due times by ±10% so checks do not bunch up.
func subscriptionJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d - d/10 + time.Duration(rand.Int63n(int64(d/5)+1))
}

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
func subscriptionNeedsCheck(a *coreauth.Auth, now time.Time, interval time.Duration) bool {
	if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "antigravity") || strings.TrimSpace(a.ProxyURL) == "" {
		return false
	}
	checked := metadataTime(a, subscriptionCheckedKey)
	return checked.IsZero() || now.Sub(checked) >= interval
}

// RequestSubscriptionCheck asks for a prompt tier re-check of one credential
// (re-auth, re-enable, subscription-quota 429, catalog change, manual). It is
// recorded in shared KV so the leader Home picks it up on its next scan
// (within a minute), still through the pinned egress and the per-minute cap.
func (r *Runtime) RequestSubscriptionCheck(authID, reason string) {
	authID = strings.TrimSpace(authID)
	if r == nil || authID == "" {
		return
	}
	now := time.Now()
	r.subTierReqMu.Lock()
	if r.subTierReq == nil {
		r.subTierReq = map[string]string{}
	}
	if r.subTierLastReq == nil || len(r.subTierLastReq) > 20000 {
		r.subTierLastReq = map[string]time.Time{}
	}
	if t, ok := r.subTierLastReq[authID]; ok && now.Sub(t) < subscriptionEventDebounce {
		r.subTierReqMu.Unlock()
		return // events (e.g. a burst of 429s) collapse into one check
	}
	r.subTierLastReq[authID] = now
	r.subTierReq[authID] = reason
	r.subTierReqMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.KVSet(ctx, subscriptionRequestKVPrefix+authID, []byte(reason), subscriptionRequestTTL, ""); err != nil {
		log.Debugf("subscription tier: queue request for %s: %v", authID, err)
	}
}

// takeSubscriptionRequests returns requested ids among live (local + shared KV).
func (r *Runtime) takeSubscriptionRequests(ctx context.Context, live []string) map[string]string {
	out := map[string]string{}
	r.subTierReqMu.Lock()
	for id, reason := range r.subTierReq {
		out[id] = reason
	}
	r.subTierReq = nil
	r.subTierReqMu.Unlock()
	for i := 0; i < len(live); i += subscriptionKVBatch {
		j := i + subscriptionKVBatch
		if j > len(live) {
			j = len(live)
		}
		keys := make([]string, 0, j-i)
		for _, id := range live[i:j] {
			keys = append(keys, subscriptionRequestKVPrefix+id)
		}
		res, err := r.KVMGet(ctx, keys)
		if err != nil {
			break
		}
		for k, v := range res {
			if v.Found && k < len(keys) {
				out[live[i+k]] = string(v.Value)
			}
		}
	}
	return out
}

// The in-memory auths carry neither ProxyURL nor full metadata, so the loop keeps its
// own next-due map and reads the full auth (DB) only when an entry is due.
func (r *Runtime) startSubscriptionTierRefresher(ctx context.Context) {
	go func() {
		tick := time.NewTicker(subscriptionScanEvery)
		defer tick.Stop()
		nextDue := map[string]time.Time{}
		lastEvent := map[string]time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if r.coreManager == nil || !r.catalogLeader() {
				continue
			}
			interval, perMin := r.subscriptionSettings()
			now := time.Now().UTC()
			lights := r.coreManager.ListLight(func(a *coreauth.Auth) bool {
				return a != nil && !a.Disabled && strings.EqualFold(a.Provider, "antigravity")
			})
			live := make(map[string]bool, len(lights))
			ids := make([]string, 0, len(lights))
			for _, a := range lights {
				live[a.ID] = true
				ids = append(ids, a.ID)
			}
			calls, reads := 0, 0
			// Event requests first (debounced), then periodic due ones.
			reqs := r.takeSubscriptionRequests(ctx, ids)
			var done []string
			for id, reason := range reqs {
				if !live[id] || calls >= perMin {
					if !live[id] {
						done = append(done, subscriptionRequestKVPrefix+id)
					}
					continue
				}
				done = append(done, subscriptionRequestKVPrefix+id)
				if t, ok := lastEvent[id]; ok && now.Sub(t) < subscriptionEventDebounce {
					continue
				}
				lastEvent[id] = now
				calls++
				if r.refreshSubscriptionTier(ctx, id, now) {
					nextDue[id] = now.Add(subscriptionJitter(interval))
				} else {
					nextDue[id] = now.Add(subscriptionRetry(interval))
				}
				log.Infof("subscription tier: event check %s (%s)", id, reason)
			}
			if len(done) > 0 {
				_, _ = r.KVDel(ctx, done)
			}
			for _, a := range lights {
				if t, ok := nextDue[a.ID]; ok && now.Before(t) {
					continue
				}
				if calls >= perMin || reads >= 10*perMin {
					break
				}
				reads++
				full := r.fullAuth(ctx, a.ID)
				if full == nil {
					continue
				}
				if !subscriptionNeedsCheck(full, now, interval) {
					if checked := metadataTime(full, subscriptionCheckedKey); !checked.IsZero() {
						nextDue[a.ID] = checked.Add(subscriptionJitter(interval))
					} else {
						nextDue[a.ID] = now.Add(subscriptionRetry(interval)) // no egress / disabled
					}
					continue
				}
				calls++
				if r.refreshSubscriptionTier(ctx, a.ID, now) {
					nextDue[a.ID] = now.Add(subscriptionJitter(interval))
				} else {
					nextDue[a.ID] = now.Add(subscriptionRetry(interval))
				}
			}
			for id := range nextDue {
				if !live[id] {
					delete(nextDue, id)
				}
			}
			for id, t := range lastEvent {
				if now.Sub(t) > subscriptionEventDebounce {
					delete(lastEvent, id)
				}
			}
		}
	}()
}

// RefreshSubscriptionTierNow checks one credential immediately (manual action).
func (r *Runtime) RefreshSubscriptionTierNow(ctx context.Context, id string) (tier, name string, ok bool) {
	if !r.refreshSubscriptionTier(ctx, id, time.Now().UTC()) {
		return "", "", false
	}
	full := r.fullAuth(ctx, id)
	if full == nil {
		return "", "", false
	}
	return metaString(full, SubscriptionTierKey), metaString(full, SubscriptionTierNameKey), true
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
	reqBody := []byte(`{"metadata":{"ide_type":"ANTIGRAVITY","ide_version":"` + agversion.Version() + `","ide_name":"antigravity"}}`)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, subscriptionLoadURL, bytes.NewReader(reqBody))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", agversion.UserAgent()+" google-api-nodejs-client/10.3.0")
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
