package home

import (
	"context"

	"net/http"
	"sort"
	"strings"
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
// quota cooldown (other buckets and credentials are untouched). The cooldown lasts two
// re-probe intervals (or until the upstream reset, if sooner) and is refreshed by each new
// probe that still shows 0%; a probe that shows remaining quota releases it at once.
// State goes through the normal auth manager path, so proxies see it from their in-memory
// auth state (no per-request lookups).

// QuotaBucketExhaustedCode marks model cooldowns set from an exhausted quota-probe bucket.
const QuotaBucketExhaustedCode = "quota_bucket_exhausted"

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
	resetAt   time.Time // latest reset among exhausted windows (zero = unknown)
}

func quotaBucketVerdicts(windows []QuotaBucketWindow, now time.Time) map[string]quotaBucketVerdict {
	out := map[string]quotaBucketVerdict{}
	for _, w := range windows {
		bucket := strings.TrimSpace(w.ScopeID)
		if (bucket != quotaBucketGemini && bucket != quotaBucketThirdParty) || w.RemainingRatio == nil {
			continue
		}
		v := out[bucket]
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
	msg := quotaBucketLabel(bucket) + " 额度耗尽（配额探测剩余 0%）"
	if !resetAt.IsZero() {
		msg += "，重置于 " + resetAt.In(quotaBucketDisplayZone).Format("01-02 15:04")
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
		until := now.Add(2 * reprobe)
		if !v.resetAt.IsZero() && v.resetAt.Before(until) {
			until = v.resetAt
		}
		for _, model := range list {
			st := auth.ModelStates[model]
			if block {
				// Skip rewriting an identical block (keeps cluster writes low).
				if isQuotaBucketState(st) && st.Unavailable && !st.NextRetryAfter.Before(until.Add(-reprobe/2)) {
					continue
				}
				out = append(out, quotaBucketAction{model: model, bucket: bucket, block: true, until: until, resetAt: v.resetAt})
				continue
			}
			// Release only cooldowns this guard set; regular 429 cooldowns keep their own path.
			if (v.known || !enabled) && isQuotaBucketState(st) && st.Unavailable && st.NextRetryAfter.After(now) {
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
	if blocked > 0 || released > 0 {
		log.Infof("quota bucket guard: %s (%s): %d model(s) blocked, %d released", auth.ID, auth.Label, blocked, released)
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
