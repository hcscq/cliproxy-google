package home

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/agversion"

	cpaconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Quota prober: Antigravity returns per-model 429 RESOURCE_EXHAUSTED without any reset
// hint, so the Home leader periodically sends one tiny request per cooling
// (credential, model). 200 clears the cooldown at once; 429 restarts the short cooldown
// (or applies the upstream hint if the probe response carries one). Probes are not
// downstream traffic: they bypass the proxies and are never recorded as key usage.

const (
	quotaProbeKVPrefix     = "quota-probe:"
	quotaProbeTick         = 2 * time.Second
	quotaProbeMaxPerSecond = 5
	quotaProbeConcurrency  = 4
	quotaProbeTimeout      = 30 * time.Second
)

var quotaProbeGeneratePath = "/v1internal:generateContent"

// QuotaProbeStatus is the last probe outcome for one (credential, model), stored in KV so
// every Home replica can show it in the panel.
type QuotaProbeStatus struct {
	At         time.Time `json:"at"`
	Result     string    `json:"result"` // ok | quota | error
	HTTPStatus int       `json:"http_status,omitempty"`
	NextProbe  time.Time `json:"next_probe"`
	Detail     string    `json:"detail,omitempty"`
}

// QuotaProbeKey is the KV key of a probe status.
func QuotaProbeKey(authID, model string) string { return quotaProbeKVPrefix + authID + "|" + model }

type quotaProber struct {
	mu      sync.Mutex
	next    map[string]time.Time
	running map[string]bool
	level   map[string]int  // probes answered "still exhausted" in the current cooldown
	carry   map[string]int  // backoff level kept across a canary unlock (key -> level)
	nudged  map[string]bool // probeNow requests: bypass skip-until-reset once
	started bool
}

// probeNow makes the (credential, model) pair (all cooling models when model is "")
// due at the next tick. Used when the quota collector sees a bucket's remaining
// fraction rise (e.g. a Google bonus reset) so hidden per-model windows are re-checked
// at once instead of at the backoff schedule.
func (p *quotaProber) probeNow(authID, model string) {
	authID = strings.TrimSpace(authID)
	if p == nil || authID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next == nil {
		return
	}
	now := time.Now()
	if p.nudged == nil {
		p.nudged = map[string]bool{}
	}
	if model != "" {
		p.next[authID+"|"+model] = now
		p.nudged[authID+"|"+model] = true
		return
	}
	for key := range p.next {
		if strings.HasPrefix(key, authID+"|") {
			p.next[key] = now
			p.nudged[key] = true
		}
	}
}

// quotaProbeBackoffStep returns the wait before the next probe of a pair that has
// already been answered "still exhausted" level times.
func quotaProbeBackoffStep(steps []time.Duration, level int) time.Duration {
	if len(steps) == 0 {
		return time.Minute
	}
	if level < 0 {
		level = 0
	}
	if level >= len(steps) {
		level = len(steps) - 1
	}
	return steps[level]
}

type quotaProbeTarget struct {
	auth  *coreauth.Auth
	model string
	reset time.Time
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

// quotaProbeRate scales the probe rate with the number of cooling pairs so a
// full pass fits in one reprobe interval, bounded by HOME_QUOTA_PROBE_RPS
// (floor, default 5) and HOME_QUOTA_PROBE_RPS_MAX (ceiling, default 50).
func quotaProbeRate(pairs int, interval time.Duration) int {
	floor := envInt("HOME_QUOTA_PROBE_RPS", quotaProbeMaxPerSecond)
	ceil := envInt("HOME_QUOTA_PROBE_RPS_MAX", 50)
	if ceil < floor {
		ceil = floor
	}
	need := floor
	if secs := int(interval / time.Second); secs > 0 {
		if n := (pairs + secs - 1) / secs; n > need {
			need = n
		}
	}
	if need > ceil {
		need = ceil
	}
	return need
}

func (r *Runtime) quotaCanaryEnabled() bool {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaCanaryEnabled()
	}
	return true
}

func (r *Runtime) quotaProbeBackoff() []time.Duration {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaProbeBackoff()
	}
	return []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
}

func (r *Runtime) quotaProbeInterval() time.Duration {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaProbeInterval()
	}
	return 60 * time.Second
}

func (r *Runtime) startQuotaProber(ctx context.Context) {
	r.prober.mu.Lock()
	if r.prober.started {
		r.prober.mu.Unlock()
		return
	}
	r.prober.started = true
	r.prober.next = map[string]time.Time{}
	r.prober.running = map[string]bool{}
	r.prober.level = map[string]int{}
	r.prober.carry = map[string]int{}
	r.prober.nudged = map[string]bool{}
	r.prober.mu.Unlock()
	go func() {
		sem := make(chan struct{}, envInt("HOME_QUOTA_PROBE_CONCURRENCY", quotaProbeConcurrency*4))
		tick := time.NewTicker(quotaProbeTick)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if !r.catalogLeader() || r.coreManager == nil {
				continue
			}
			if n := r.coreManager.NormalizeModelScopedErrorStatuses(ctx); n > 0 {
				log.Infof("auth status: %d credential(s) with only per-model failures set back to active", n)
			}
			due, rate := r.dueQuotaProbes(time.Now())
			gap := time.Second / time.Duration(rate)
			for _, target := range due {
				select {
				case <-ctx.Done():
					return
				case <-time.After(gap):
				}
				sem <- struct{}{}
				go func(t quotaProbeTarget) {
					defer func() { <-sem }()
					r.runQuotaProbe(ctx, t)
				}(target)
			}
		}
	}()
}

// hasCoolingQuotaPair is the read-only pre-filter used under the manager lock
// so only cooling credentials are cloned.
func hasCoolingQuotaPair(a *coreauth.Auth, now time.Time) bool {
	if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "antigravity") {
		return false
	}
	for _, st := range a.ModelStates {
		if st != nil && st.Unavailable && st.NextRetryAfter.After(now) && st.LastError != nil && st.LastError.HTTPStatus == http.StatusTooManyRequests {
			return true
		}
	}
	return false
}

// coolingQuotaPairs lists (credential, model) pairs under a model-scoped 429 cooldown.
func coolingQuotaPairs(auths []*coreauth.Auth, now time.Time) []quotaProbeTarget {
	var out []quotaProbeTarget
	for _, a := range auths {
		if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "antigravity") {
			continue
		}
		for model, st := range a.ModelStates {
			if st == nil || !st.Unavailable || !st.NextRetryAfter.After(now) {
				continue
			}
			if st.LastError == nil || st.LastError.HTTPStatus != http.StatusTooManyRequests {
				continue
			}
			if st.Quota.Reason == "credential_quota" || st.LastError.Code == QuotaBucketExhaustedCode || st.LastError.Code == QuotaGroupExhaustedCode || st.LastError.Code == QuotaGroupLowCode {
				// Bucket blocks are re-checked by the quota collector, not per-model probes.
				continue
			}
			// Long upstream resets are probed too (on the backoff schedule, capped at a
			// few probes per hour): Google grants early/bonus resets, and releasing them
			// on bucket ratios instead sent real traffic into hidden per-model windows.
			out = append(out, quotaProbeTarget{auth: a, model: model, reset: st.NextRetryAfter})
		}
	}
	return out
}

func (r *Runtime) dueQuotaProbes(now time.Time) ([]quotaProbeTarget, int) {
	pairs := coolingQuotaPairs(r.coreManager.ListMatching(func(a *coreauth.Auth) bool { return hasCoolingQuotaPair(a, now) }), now)
	pairs = r.dropUnavailableModelPairs(pairs)
	pairs = r.dropReservedProbePairs(pairs)
	r.syncQuotaGroupRules()
	skipFar := r.quotaProbeSkipThreshold()
	interval := r.quotaProbeInterval()
	steps := r.quotaProbeBackoff()
	r.prober.mu.Lock()
	defer r.prober.mu.Unlock()
	live := make(map[string]bool, len(pairs))
	rate := quotaProbeRate(len(pairs), interval)
	budget := rate * int((quotaProbeTick+time.Second-1)/time.Second)
	// Nearest reset first: those credentials come back soonest.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].reset.Before(pairs[j].reset) })
	pairs = filterQuotaProbePairs(pairs, r.prober.nudged, skipFar, now)
	var due []quotaProbeTarget
	for _, p := range pairs {
		key := p.auth.ID + "|" + p.model
		live[key] = true
		next, known := r.prober.next[key]
		if !known {
			// First sight (incl. right after deploy): first backoff step, spread to avoid bursts.
			first := quotaProbeBackoffStep(steps, 0)
			next = now.Add(first/2 + time.Duration(rand.Int63n(int64(first)/2+1)))
			lvl := 0
			if c, ok := r.prober.carry[key]; ok {
				// Re-blocked after a failed canary: continue the backoff, don't restart it.
				lvl = c
				next = now.Add(jitter(quotaProbeBackoffStep(steps, lvl)))
				delete(r.prober.carry, key)
			}
			r.prober.next[key] = next
			r.prober.level[key] = lvl
		}
		if r.prober.running[key] || now.Before(next) || len(due) >= budget {
			continue
		}
		r.prober.running[key] = true
		delete(r.prober.nudged, key)
		due = append(due, p)
	}
	for key := range r.prober.next {
		if !live[key] && !r.prober.running[key] {
			delete(r.prober.next, key)
			delete(r.prober.level, key)
		}
	}
	return due, rate
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// ±20%
	return d + time.Duration(rand.Int63n(int64(d)/5*2+1)) - d/5
}

func (r *Runtime) runQuotaProbe(ctx context.Context, t quotaProbeTarget) {
	key := t.auth.ID + "|" + t.model
	steps := r.quotaProbeBackoff()
	started := time.Now()
	status := QuotaProbeStatus{At: started.UTC()}
	var probeCode int
	var probeBody []byte
	defer func() {
		r.recordProbeUsage(t, started, probeCode, probeBody, status.Result)
		r.prober.mu.Lock()
		delete(r.prober.running, key)
		level := r.prober.level[key]
		switch status.Result {
		case "quota":
			level++
			r.prober.level[key] = level
		case "ok":
			delete(r.prober.level, key)
			level = 0
		case "canary":
			// Not proven yet: keep the step; a canary QUOTA_EXHAUSTED advances it.
			r.prober.carry[key] = level
		}
		next := time.Now().Add(jitter(quotaProbeBackoffStep(steps, level)))
		r.prober.next[key] = next
		r.prober.mu.Unlock()
		status.NextProbe = next.UTC()
		if raw, err := json.Marshal(status); err == nil {
			kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = r.KVSet(kctx, QuotaProbeKey(t.auth.ID, t.model), raw, 2*time.Hour, "")
			cancel()
		}
	}()

	pctx, cancel := context.WithTimeout(ctx, quotaProbeTimeout)
	defer cancel()
	full := t.auth
	if extractAccessToken(full) == "" && r.clusterAdapter != nil && r.clusterAdapter.Enabled() {
		if loaded, err := r.clusterAdapter.GetFullAuth(pctx, t.auth.ID); err == nil && loaded != nil {
			full = loaded
		}
	}
	code, header, body, err := sendQuotaProbe(pctx, full, t.model)
	probeCode, probeBody = code, body
	status.HTTPStatus = code
	switch {
	case err != nil:
		status.Result, status.Detail = "error", err.Error()
	case code >= 200 && code < 300:
		status.Result = "ok"
		if _, errClear := r.coreManager.ClearQuotaCooldown(pctx, t.auth.ID, t.model); errClear != nil {
			status.Detail = "clear failed: " + errClear.Error()
		}
		if r.quotaCanaryEnabled() && t.reset.After(time.Now().Add(5*time.Second)) {
			// Before the known reset a 1-token probe can pass while real requests still
			// get QUOTA_EXHAUSTED: hold the pair and let one real request decide.
			status.Result = "canary"
			r.armQuotaCanary(t.auth.ID, t.model, t.reset)
			log.Infof("quota probe: %s %s passed before reset %s; canary armed", t.auth.ID, t.model, t.reset.UTC().Format(time.RFC3339))
			break
		}
		log.Infof("quota probe: %s %s recovered, cooldown cleared", t.auth.ID, t.model)
		r.releaseQuotaGroupSiblings(pctx, t.auth.ID, t.model)
	case code == http.StatusTooManyRequests:
		status.Result = "quota"
		res := coreauth.Result{AuthID: t.auth.ID, Provider: "antigravity", Model: t.model,
			Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota probe: still exhausted"}, RestartQuotaWindow: true}
		if coreauth.IsModelQuotaExhaustedBody("antigravity", string(body)) {
			res.Error.Code = coreauth.ModelQuotaExhaustedCode
			res.Error.Message = "模型额度耗尽（探测）"
		}
		if d, at := quotaProbeHint(header, body, time.Now()); d != nil {
			res.RetryAfter = d
			status.Detail = "upstream hint " + d.Round(time.Second).String()
		} else if at != nil {
			res.ResetAt = at
			status.Detail = "upstream reset " + at.UTC().Format(time.RFC3339)
		}
		r.coreManager.MarkResult(pctx, res)
	default:
		// Other failures (401, 5xx...) are not quota answers; leave the cooldown alone.
		status.Result = "error"
		status.Detail = strings.TrimSpace(string(body))
		if len(status.Detail) > 160 {
			status.Detail = status.Detail[:160]
		}
	}
}

func sendQuotaProbe(ctx context.Context, auth *coreauth.Auth, model string) (int, http.Header, []byte, error) {
	token := extractAccessToken(auth)
	if token == "" {
		return 0, nil, nil, errAntigravityNoToken
	}
	project, _ := auth.Metadata["project_id"].(string)
	payload := map[string]any{
		"model": model, "project": project, "userAgent": "antigravity", "requestType": "agent",
		"request": map[string]any{
			"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		},
	}
	body, _ := json.Marshal(payload)
	base := antigravityDefaultModelsBaseURL
	if b, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
		base = strings.TrimRight(strings.TrimSpace(b), "/")
	}
	if strings.HasSuffix(antigravityModelsURL, antigravityModelsEndpointPath) && base == antigravityDefaultModelsBaseURL {
		base = strings.TrimSuffix(antigravityModelsURL, antigravityModelsEndpointPath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+quotaProbeGeneratePath, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", agversion.UserAgent())
	resp, err := credentialHTTPClient(auth, quotaProbeTimeout).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, resp.Header, data, nil
}

// quotaProbeHint extracts an upstream retry hint from a probe 429.
func quotaProbeHint(header http.Header, body []byte, now time.Time) (*time.Duration, *time.Time) {
	for _, d := range gjson.GetBytes(body, "error.details").Array() {
		if v := d.Get("retryDelay").String(); v != "" {
			if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
				return &dur, nil
			}
		}
		if v := d.Get("metadata.quotaResetDelay").String(); v != "" {
			if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
				return &dur, nil
			}
		}
		if v := d.Get("metadata.quotaResetTimeStamp").String(); v != "" {
			if at, err := time.Parse(time.RFC3339, v); err == nil && at.After(now) {
				return nil, &at
			}
		}
	}
	if header != nil {
		if v := strings.TrimSpace(header.Get("Retry-After")); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				dur := time.Duration(secs) * time.Second
				return &dur, nil
			}
		}
	}
	return nil, nil
}

// recordProbeUsage logs one probe as a usage row (event_type "probe", executor
// "QuotaProbe") so probes are countable; it is persisted only, never fed back into
// scheduler state (the probe outcome is applied explicitly above).
func (r *Runtime) recordProbeUsage(t quotaProbeTarget, started time.Time, code int, body []byte, result string) {
	if r == nil || t.auth == nil {
		return
	}
	failed := !(code >= 200 && code < 300)
	payload := map[string]any{
		"usage_id":      "probe-" + t.auth.ID + "-" + t.model + "-" + strconv.FormatInt(started.UnixNano(), 36),
		"request_id":    "probe-" + strconv.FormatInt(started.UnixNano(), 36),
		"timestamp":     started.UTC().Format(time.RFC3339Nano),
		"event_type":    "probe",
		"provider":      "antigravity",
		"executor_type": "QuotaProbe",
		"model":         t.model,
		"alias":         t.model,
		"auth_index":    t.auth.ID,
		"auth_id":       t.auth.ID,
		"source":        t.auth.Label,
		"latency_ms":    time.Since(started).Milliseconds(),
		"failed":        failed,
	}
	if failed {
		b := string(body)
		if len(b) > 2000 {
			b = b[:2000]
		}
		if code <= 0 {
			b = "probe " + result
		}
		payload["fail"] = map[string]any{"status_code": code, "body": b}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = r.PersistClusterUsagePayload(ctx, string(raw), time.Now().UTC())
}

// dropUnavailableModelPairs skips cooling pairs whose model is on the panel's
// upstream-unavailable list (antigravity health.unavailable-models): proxies
// answer those models locally with 503, so probing them only burns upstream
// calls and keeps re-cooling credentials.
func (r *Runtime) dropUnavailableModelPairs(pairs []quotaProbeTarget) []quotaProbeTarget {
	var spec *string
	if cfg := r.Config(); cfg != nil {
		spec = cfg.Antigravity.Health.UnavailableModels
	}
	return filterUnavailableModelPairs(pairs, spec)
}

func filterUnavailableModelPairs(pairs []quotaProbeTarget, spec *string) []quotaProbeTarget {
	pats := cpaconfig.ParseUnavailableModels(spec)
	if len(pats) == 0 {
		return pairs
	}
	out := pairs[:0:0]
	for _, p := range pairs {
		if cpaconfig.MatchUnavailableModel(pats, p.model) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// dropReservedProbePairs skips cooldown probes (generateContent) on reserved credentials
// unless routing.reserve.ai-probe is on.
func (r *Runtime) dropReservedProbePairs(pairs []quotaProbeTarget) []quotaProbeTarget {
	if r.reserveSettings().AIProbe || len(pairs) == 0 {
		return pairs
	}
	return filterReservedPairs(pairs, r.ReservedSince())
}

func filterReservedPairs(pairs []quotaProbeTarget, reserved map[string]time.Time) []quotaProbeTarget {
	if len(reserved) == 0 {
		return pairs
	}
	out := pairs[:0:0]
	for _, p := range pairs {
		if p.auth != nil {
			if _, ok := reserved[p.auth.ID]; ok {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
