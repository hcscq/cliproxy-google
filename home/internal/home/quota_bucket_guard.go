package home

import (
	"context"

	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Quota bucket guard: the active quota probe (quota collector) reports per-bucket remaining
// quota for Antigravity credentials ("Gemini models", "Claude and GPT models"). Without this
// guard a bucket at 0% kept receiving traffic, and every request paid one upstream 429 before
// the per-model short cooldown kicked in (and again after each 60s window). When a probe shows
// a bucket at 0%, every model of that bucket on that credential is put into a model-scoped
// quota cooldown (other buckets and credentials are untouched). The cooldown lasts until the
// upstream reset (or two re-probe intervals when no reset is reported) and is refreshed by each new
// probe that still shows 0%; a probe that shows remaining quota releases it at once.
// State goes through the normal auth manager path, so proxies see it from their in-memory
// auth state (no per-request lookups).

// QuotaBucketExhaustedCode marks model cooldowns set from an exhausted quota-probe bucket.
const QuotaBucketExhaustedCode = "quota_bucket_exhausted"

// quotaBucketMaxHorizon bounds how far a probe-reported reset may push a cooldown.
const quotaBucketMaxHorizon = 30 * 24 * time.Hour

const (
	quotaBucketGemini     = "gemini"
	quotaBucketThirdParty = "third-party"
)

// QuotaBucketWindow is one probed quota window, reduced to what the guard needs.
type QuotaBucketWindow struct {
	ScopeID        string
	RemainingRatio *float64
	ResetAt        *time.Time
}

// antigravityModelBucket maps an Antigravity model id to its quota bucket ("" = unknown).
func antigravityModelBucket(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"), strings.Contains(m, "gpt"):
		return quotaBucketThirdParty
	case strings.HasPrefix(m, "gemini"):
		return quotaBucketGemini
	default:
		return ""
	}
}

func quotaBucketLabel(bucket string) string {
	if bucket == quotaBucketThirdParty {
		return "Claude/GPT"
	}
	return "Gemini"
}

type quotaBucketVerdict struct {
	known     bool
	exhausted bool
	ratio     float64   // lowest remaining ratio among the bucket's windows
	resetAt   time.Time // latest reset among exhausted windows (zero = unknown)
}

func quotaBucketVerdicts(windows []QuotaBucketWindow, now time.Time) map[string]quotaBucketVerdict {
	out := map[string]quotaBucketVerdict{}
	for _, w := range windows {
		bucket := strings.TrimSpace(w.ScopeID)
		if (bucket != quotaBucketGemini && bucket != quotaBucketThirdParty) || w.RemainingRatio == nil {
			continue
		}
		v, seen := out[bucket]
		if !seen || *w.RemainingRatio < v.ratio {
			v.ratio = *w.RemainingRatio
		}
		v.known = true
		if *w.RemainingRatio <= 0 {
			v.exhausted = true
			if w.ResetAt != nil && w.ResetAt.After(now) && w.ResetAt.After(v.resetAt) {
				v.resetAt = w.ResetAt.UTC()
			}
		}
		out[bucket] = v
	}
	return out
}

var quotaBucketDisplayZone = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("UTC+8", 8*3600)
}()

func quotaBucketMessage(bucket string, resetAt time.Time) string {
	msg := quotaBucketLabel(bucket) + " 额度耗尽"
	if !resetAt.IsZero() {
		msg += " · 重置 " + resetAt.In(quotaBucketDisplayZone).Format("01-02 15:04")
	}
	return msg
}

// credentialBucketModels lists the credential's models (registered + already tracked) per bucket.
func credentialBucketModels(auth *coreauth.Auth) map[string][]string {
	seen := map[string]bool{}
	out := map[string][]string{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		if b := antigravityModelBucket(model); b != "" {
			out[b] = append(out[b], model)
		}
	}
	for _, info := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if info != nil {
			add(info.ID)
		}
	}
	for model := range auth.ModelStates {
		add(model)
	}
	for b := range out {
		sort.Strings(out[b])
	}
	return out
}

func isQuotaBucketState(st *coreauth.ModelState) bool {
	return st != nil && st.LastError != nil && st.LastError.Code == QuotaBucketExhaustedCode
}

// quotaBucketPlan computes which models to block (with deadline) and which to release.
type quotaBucketAction struct {
	model   string
	bucket  string
	block   bool
	until   time.Time
	resetAt time.Time
}

func planQuotaBucketActions(auth *coreauth.Auth, models map[string][]string, verdicts map[string]quotaBucketVerdict, reprobe time.Duration, enabled bool, now time.Time) []quotaBucketAction {
	var out []quotaBucketAction
	for bucket, list := range models {
		v := verdicts[bucket]
		block := enabled && v.known && v.exhausted
		// Block until the upstream reset when the probe reports one (re-probes release
		// early if quota comes back); without a reset fall back to two re-probe intervals.
		until := now.Add(2 * reprobe)
		if !v.resetAt.IsZero() && v.resetAt.After(now) && v.resetAt.Sub(now) <= quotaBucketMaxHorizon {
			until = v.resetAt
		}
		for _, model := range list {
			st := auth.ModelStates[model]
			if block {
				// Never shorten an existing quota cooldown (e.g. a request-path 429 that
				// already carries the weekly reset), and skip rewriting an identical block.
				if st != nil && st.Unavailable && (st.Quota.Exceeded || isQuotaBucketState(st)) && !st.NextRetryAfter.Before(until.Add(-reprobe/2)) {
					continue
				}
				out = append(out, quotaBucketAction{model: model, bucket: bucket, block: true, until: until, resetAt: v.resetAt})
				continue
			}
			if st == nil || !st.Unavailable || !st.NextRetryAfter.After(now) {
				continue
			}
			// Release cooldowns this guard set, and (when the probe shows quota back) long
			// quota cooldowns from upstream reset hints. Short 429 cooldowns keep their own path.
			guardState := isQuotaBucketState(st)
			// An explicit per-model QUOTA_EXHAUSTED is a hidden per-model window: the bucket
			// ratio says nothing about it, so it is never released here (the per-model prober
			// re-checks it, and is nudged when the bucket ratio rises).
			hiddenModelQuota := st.LastError != nil && st.LastError.Code == coreauth.ModelQuotaExhaustedCode
			longQuota := enabled && v.known && st.Quota.Exceeded && !hiddenModelQuota && st.NextRetryAfter.Sub(now) > 2*reprobe
			if (guardState && (v.known || !enabled)) || longQuota {
				out = append(out, quotaBucketAction{model: model, bucket: bucket})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].model < out[j].model })
	return out
}

// ApplyQuotaBucketWindows applies one fresh quota-probe result of an Antigravity credential.
func (r *Runtime) ApplyQuotaBucketWindows(ctx context.Context, authID string, windows []QuotaBucketWindow) {
	if r == nil || r.coreManager == nil {
		return
	}
	auth, ok := r.coreManager.GetByID(authID)
	if !ok || auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
		return
	}
	reprobe, enabled := r.QuotaBucketReprobeInterval()
	now := time.Now().UTC()
	actions := planQuotaBucketActions(auth, credentialBucketModels(auth), quotaBucketVerdicts(windows, now), reprobe, enabled, now)
	blocked, released := 0, 0
	for _, a := range actions {
		if a.block {
			until := a.until
			r.coreManager.MarkResult(ctx, coreauth.Result{
				AuthID: auth.ID, Provider: "antigravity", Model: a.model, ResetAt: &until, RestartQuotaWindow: true,
				Error: &coreauth.Error{Code: QuotaBucketExhaustedCode, HTTPStatus: http.StatusTooManyRequests, Message: quotaBucketMessage(a.bucket, a.resetAt)},
			})
			blocked++
			continue
		}
		if _, err := r.coreManager.ClearQuotaCooldown(ctx, auth.ID, a.model); err != nil {
			log.WithError(err).Warnf("quota bucket guard: release %s %s failed", auth.ID, a.model)
			continue
		}
		released++
	}
	probed := r.nudgeProbesOnBucketRise(auth, quotaBucketVerdicts(windows, now), now)
	if blocked > 0 || released > 0 || probed > 0 {
		log.Infof("quota bucket guard: %s (%s): %d model(s) blocked, %d released, %d probed now (bucket rising)", auth.ID, auth.Label, blocked, released, probed)
	}
}

// QuotaBucketReprobeInterval returns the configured re-probe interval for exhausted buckets.
func (r *Runtime) QuotaBucketReprobeInterval() (time.Duration, bool) {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaBucketReprobeInterval()
	}
	return 5 * time.Minute, true
}

// QuotaBucketExhausted reports whether any probed bucket window is at 0%.
func QuotaBucketExhausted(windows []QuotaBucketWindow) bool {
	for _, v := range quotaBucketVerdicts(windows, time.Now()) {
		if v.exhausted {
			return true
		}
	}
	return false
}

// bucketRatios remembers the last remaining ratio per (credential, bucket).
var bucketRatios sync.Map // "authID|bucket" -> float64

// nudgeProbesOnBucketRise makes every cooling per-model 429 pair of a bucket whose
// remaining ratio rose since the previous collector run due for an immediate probe
// (e.g. Google granted a bonus/early reset). Returns the number of pairs nudged.
func (r *Runtime) nudgeProbesOnBucketRise(auth *coreauth.Auth, verdicts map[string]quotaBucketVerdict, now time.Time) int {
	if auth == nil {
		return 0
	}
	rising := map[string]bool{}
	for bucket, v := range verdicts {
		if !v.known {
			continue
		}
		key := auth.ID + "|" + bucket
		prev, had := bucketRatios.Load(key)
		bucketRatios.Store(key, v.ratio)
		if had && v.ratio > prev.(float64)+0.0001 {
			rising[bucket] = true
		}
	}
	if len(rising) == 0 {
		return 0
	}
	n := 0
	for model, st := range auth.ModelStates {
		if st == nil || !st.Unavailable || !st.NextRetryAfter.After(now) || st.LastError == nil || st.LastError.HTTPStatus != http.StatusTooManyRequests || isQuotaBucketState(st) {
			continue
		}
		if !rising[antigravityModelBucket(model)] {
			continue
		}
		r.prober.probeNow(auth.ID, model)
		publishQuotaSync(quotaSyncMsg{Kind: "probe", AuthID: auth.ID, Model: model})
		n++
	}
	return n
}
