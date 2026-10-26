package home

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Quota groups: Antigravity meters models in a few shared quota groups per credential
// (Gemini general, Claude/GPT-OSS, and on Pro accounts a separate gemini-4-argon group).
// A model's group is resolved as panel override > group learned from the credential's
// catalog (models with an identical remainingFraction+resetTime signature) > none
// (per-model). Quota-type 429s and exhausted catalog groups cool the whole group; capacity
// / short rate-limit 429s stay per-model. The prober probes one model per group and does
// not probe before a known far-away reset (catalog refresh still catches early resets).

const (
	// QuotaGroupExhaustedCode marks cooldowns propagated to the other models of a quota group.
	QuotaGroupExhaustedCode = "quota_group_exhausted"
	// QuotaGroupLowCode marks a soft skip of a credential whose group quota is nearly used up.
	QuotaGroupLowCode = "quota_group_low"

	defaultQuotaGroupOverrides = "ultra: gemini-*=gemini; *: gemini-4-argon*=argon; *: claude-*|gpt-oss-*=3p; *: gemini-*=gemini"
	quotaGroupMinQuotaCooldown = 10 * time.Minute
	quotaGroupLowMinHealthy    = 3
)

type quotaGroupRule struct {
	tiers []string // empty = any
	pats  []string
	group string
}

// parseQuotaGroupOverrides parses "tier[,tier]: pat|pat=group" rules (';' or newline separated).
func parseQuotaGroupOverrides(raw string) ([]quotaGroupRule, []string) {
	var rules []quotaGroupRule
	var errs []string
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "off") {
		return nil, nil
	}
	if raw == "" {
		raw = defaultQuotaGroupOverrides
	}
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ';' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var rule quotaGroupRule
		if i := strings.Index(line, ":"); i >= 0 {
			for _, t := range strings.Split(strings.ToLower(line[:i]), ",") {
				if t = strings.TrimSpace(t); t != "" && t != "*" {
					rule.tiers = append(rule.tiers, t)
				}
			}
			line = line[i+1:]
		}
		eq := strings.LastIndex(line, "=")
		if eq <= 0 {
			errs = append(errs, line)
			continue
		}
		rule.group = strings.TrimSpace(line[eq+1:])
		for _, p := range strings.Split(strings.ToLower(line[:eq]), "|") {
			if p = strings.TrimSpace(p); p != "" {
				if _, err := path.Match(p, "x"); err != nil {
					errs = append(errs, p)
					continue
				}
				rule.pats = append(rule.pats, p)
			}
		}
		if rule.group == "" || len(rule.pats) == 0 {
			errs = append(errs, line)
			continue
		}
		rules = append(rules, rule)
	}
	return rules, errs
}

func normQuotaModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

func matchQuotaGroupRules(rules []quotaGroupRule, tier, model string) string {
	m := normQuotaModel(model)
	for _, r := range rules {
		if len(r.tiers) > 0 {
			ok := false
			for _, t := range r.tiers {
				if t == tier {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		for _, p := range r.pats {
			if ok, _ := path.Match(p, m); ok {
				return r.group
			}
		}
	}
	return ""
}

type quotaGroupRuleSet struct {
	raw   string
	rules []quotaGroupRule
}

var quotaGroupRulesCur atomic.Pointer[quotaGroupRuleSet]

func currentQuotaGroupRules() []quotaGroupRule {
	if cur := quotaGroupRulesCur.Load(); cur != nil {
		return cur.rules
	}
	rules, _ := parseQuotaGroupOverrides("")
	quotaGroupRulesCur.Store(&quotaGroupRuleSet{rules: rules})
	return rules
}

// Tier cache: the scheduler's auth copies do not always carry the subscription tier, so
// it is loaded once per credential from the full record in the background (unknown tier
// keeps argon in its own group, the safe side) and refreshed by every subscription check.
var (
	quotaTierCache   sync.Map // authID -> tier class
	quotaTierLoading sync.Map // authID -> time.Time of last attempt
	quotaTierRuntime atomic.Pointer[Runtime]
	quotaTierSem     = make(chan struct{}, 4)
)

func setQuotaTier(authID, tier string) {
	if authID != "" && tier != "" {
		quotaTierCache.Store(authID, reserveTierClass(tier))
	}
}

func quotaTierOf(auth *coreauth.Auth) string {
	if auth == nil {
		return "unknown"
	}
	if t := stringMeta(auth, SubscriptionTierKey); t != "" {
		return reserveTierClass(t)
	}
	if v, ok := quotaTierCache.Load(auth.ID); ok {
		return v.(string)
	}
	rt := quotaTierRuntime.Load()
	if rt == nil {
		return "unknown"
	}
	now := time.Now()
	if v, ok := quotaTierLoading.Load(auth.ID); ok && now.Sub(v.(time.Time)) < 5*time.Minute {
		return "unknown"
	}
	quotaTierLoading.Store(auth.ID, now)
	id := auth.ID
	go func() {
		quotaTierSem <- struct{}{}
		defer func() { <-quotaTierSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if full := rt.fullAuth(ctx, id); full != nil {
			if t := stringMeta(full, SubscriptionTierKey); t != "" {
				setQuotaTier(id, t)
				quotaTierLoading.Delete(id)
			}
		}
	}()
	return "unknown"
}

// syncQuotaGroupRules re-parses the override setting when it changed.
func (r *Runtime) syncQuotaGroupRules() {
	if r != nil && quotaTierRuntime.Load() != r {
		quotaTierRuntime.Store(r)
	}
	raw := ""
	if r != nil {
		if cfg := r.Config(); cfg != nil {
			raw = cfg.QuotaGroupOverrides
		}
	}
	if cur := quotaGroupRulesCur.Load(); cur != nil && cur.raw == raw {
		return
	}
	rules, errs := parseQuotaGroupOverrides(raw)
	if len(errs) > 0 {
		log.Warnf("quota groups: ignored invalid override rule(s): %s", strings.Join(errs, " ; "))
	}
	quotaGroupRulesCur.Store(&quotaGroupRuleSet{raw: raw, rules: rules})
}

// ---- learning ----

var (
	learnedQuotaGroupsByAuth sync.Map // authID -> map[model]group
	learnedQuotaGroupsByTier sync.Map // tier|model -> group
)

func ignoredQuotaModel(m string) bool {
	return strings.HasPrefix(m, "chat_") || strings.HasPrefix(m, "tab_")
}

// learnQuotaGroups groups catalog models by identical (remainingFraction, resetTime).
// Full (1.0) signatures are indistinguishable and skipped.
func learnQuotaGroups(h *antigravityCatalogHealth) map[string]string {
	if h == nil {
		return nil
	}
	bySig := map[string][]string{}
	for id, m := range h.models {
		id = normQuotaModel(id)
		if ignoredQuotaModel(id) || m.QuotaRemaining == nil || *m.QuotaRemaining >= 0.9999 {
			continue
		}
		sig := fmt.Sprintf("%.4f|%s", *m.QuotaRemaining, m.QuotaResetTime)
		bySig[sig] = append(bySig[sig], id)
	}
	out := map[string]string{}
	for _, members := range bySig {
		sort.Strings(members)
		name := "auto:" + members[0]
		for _, id := range members {
			out[id] = name
		}
	}
	return out
}

func (r *Runtime) recordLearnedQuotaGroups(auth *coreauth.Auth, h *antigravityCatalogHealth) {
	if auth == nil || h == nil {
		return
	}
	learned := learnQuotaGroups(h)
	learnedQuotaGroupsByAuth.Store(auth.ID, learned)
	tier := quotaTierOf(auth)
	// Tier pattern: only multi-member groups say something about other credentials.
	size := map[string]int{}
	for _, g := range learned {
		size[g]++
	}
	for m, g := range learned {
		if size[g] > 1 {
			learnedQuotaGroupsByTier.Store(tier+"|"+m, g)
		}
	}
}

// QuotaGroupFor resolves a model's quota group on a credential ("" = per-model) and its source.
func QuotaGroupFor(auth *coreauth.Auth, model string) (string, string) {
	if auth == nil {
		return "", ""
	}
	tier := quotaTierOf(auth)
	m := normQuotaModel(model)
	if ignoredQuotaModel(m) {
		return "", ""
	}
	if g := matchQuotaGroupRules(currentQuotaGroupRules(), tier, m); g != "" {
		return g, "override"
	}
	if v, ok := learnedQuotaGroupsByAuth.Load(auth.ID); ok {
		if g := v.(map[string]string)[m]; g != "" {
			return g, "learned"
		}
	}
	if v, ok := learnedQuotaGroupsByTier.Load(tier + "|" + m); ok {
		return v.(string), "learned-tier"
	}
	return "", ""
}

func quotaGroupOf(auth *coreauth.Auth, model string) string {
	g, _ := QuotaGroupFor(auth, model)
	return g
}

// credentialModels lists a credential's registered + tracked models.
func credentialModels(auth *coreauth.Auth) []string {
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, info := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if info != nil {
			add(info.ID)
		}
	}
	for m := range auth.ModelStates {
		add(m)
	}
	sort.Strings(out)
	return out
}

func credentialGroupModels(auth *coreauth.Auth, group string) []string {
	if group == "" {
		return nil
	}
	var out []string
	for _, m := range credentialModels(auth) {
		if quotaGroupOf(auth, m) == group {
			out = append(out, m)
		}
	}
	return out
}

func (r *Runtime) quotaGroupCooldownEnabled() bool {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaGroupCooldownEnabled()
	}
	return true
}

func (r *Runtime) quotaProbeSkipThreshold() time.Duration {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaProbeSkipThreshold()
	}
	return 30 * time.Minute
}

func (r *Runtime) quotaLowRemaining() float64 {
	if cfg := r.Config(); cfg != nil {
		return cfg.QuotaLowRemainingThreshold()
	}
	return 0.03
}

// ---- request-path propagation ----

var resetsInRe = regexp.MustCompile(`(?i)resets? in ([0-9][0-9hms.]*)`)

// quotaTypeUntil reports whether a 429 is a quota-window exhaustion (not capacity / short
// rate limit) and until when.
func quotaTypeUntil(result coreauth.Result, body string, now time.Time) (time.Time, bool) {
	if result.Success || result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	low := strings.ToLower(body)
	if strings.Contains(low, "capacity on this model") || strings.Contains(low, "model_capacity_exhausted") {
		return time.Time{}, false
	}
	m := resetsInRe.FindStringSubmatch(body)
	if result.Error.Code != coreauth.ModelQuotaExhaustedCode && m == nil {
		return time.Time{}, false
	}
	until := quotaCooldownUntil(result, now)
	if until.Sub(now) < quotaGroupMinQuotaCooldown && m != nil {
		if d, err := time.ParseDuration(strings.TrimRight(m[1], ".")); err == nil {
			until = now.Add(d)
		}
	}
	if until.Sub(now) < quotaGroupMinQuotaCooldown {
		return time.Time{}, false
	}
	if until.Sub(now) > quotaBucketMaxHorizon {
		until = now.Add(quotaBucketMaxHorizon)
	}
	return until, true
}

var quotaGroupPropagated, quotaGroupCatalogBlocked, quotaGroupReleased, quotaGroupLowBlocked, quotaProbeSkipped, quotaProbeGroupDeduped atomic.Int64

// QuotaGroupStats exposes counters for the panel / logs.
func QuotaGroupStats() map[string]int64 {
	return map[string]int64{
		"propagated": quotaGroupPropagated.Load(), "catalog_blocked": quotaGroupCatalogBlocked.Load(),
		"released": quotaGroupReleased.Load(), "low_blocked": quotaGroupLowBlocked.Load(),
		"probe_skipped": quotaProbeSkipped.Load(), "probe_group_deduped": quotaProbeGroupDeduped.Load(),
	}
}

func quotaGroupMessage(group string, resetAt time.Time) string {
	msg := "额度组 " + group + " 耗尽"
	if !resetAt.IsZero() {
		msg += " · 重置 " + resetAt.In(quotaBucketDisplayZone).Format("01-02 15:04")
	}
	return msg
}

// groupBlock cools one sibling model (local state + fast block on every Home).
func (r *Runtime) groupBlock(ctx context.Context, auth *coreauth.Auth, model, code, msg string, until time.Time) bool {
	if st := auth.ModelStates[model]; st != nil && st.Unavailable && !st.NextRetryAfter.Before(until.Add(-time.Minute)) {
		return false
	}
	u := until
	r.coreManager.MarkResult(ctx, coreauth.Result{
		AuthID: auth.ID, Provider: "antigravity", Model: model, ResetAt: &u, RestartQuotaWindow: true,
		Error: &coreauth.Error{Code: code, HTTPStatus: http.StatusTooManyRequests, Message: msg},
	})
	coreauth.MarkQuotaBlocked(auth.ID, model, until)
	publishQuotaSync(quotaSyncMsg{Kind: "cool", AuthID: auth.ID, Model: model, UntilMS: until.UnixMilli(), Code: code, Message: msg})
	return true
}

// propagateQuotaGroupCooldown cools the rest of the quota group after a quota-type 429.
func (r *Runtime) propagateQuotaGroupCooldown(ctx context.Context, authID string, result coreauth.Result, body string) int {
	if r == nil || r.coreManager == nil || !r.quotaGroupCooldownEnabled() || r.coreManager.QuotaCooldownDisabledFor(authID) {
		return 0
	}
	until, ok := quotaTypeUntil(result, body, time.Now())
	if !ok {
		return 0
	}
	auth, found := r.coreManager.GetByID(authID)
	if !found || auth == nil || !strings.EqualFold(auth.Provider, "antigravity") {
		return 0
	}
	r.syncQuotaGroupRules()
	group := quotaGroupOf(auth, result.Model)
	if group == "" {
		return 0
	}
	n := 0
	msg := quotaGroupMessage(group, until)
	for _, m := range credentialGroupModels(auth, group) {
		if strings.EqualFold(m, result.Model) {
			continue
		}
		if r.groupBlock(ctx, auth, m, QuotaGroupExhaustedCode, msg, until) {
			n++
		}
	}
	if n > 0 {
		quotaGroupPropagated.Add(int64(n))
		log.Infof("quota group: %s %s exhausted (via %s) until %s; %d sibling model(s) cooled", auth.ID, group, result.Model, until.Format(time.RFC3339), n)
	}
	return n
}

// ---- catalog-driven guard ----

type catalogGroupVerdict struct {
	models    []string
	exhausted bool // every catalog model of the group shows 0
	ratio     float64
	resetAt   time.Time
}

func catalogGroupVerdicts(auth *coreauth.Auth, h *antigravityCatalogHealth, now time.Time) map[string]*catalogGroupVerdict {
	out := map[string]*catalogGroupVerdict{}
	for id, m := range h.models {
		if m.QuotaRemaining == nil {
			continue
		}
		g := quotaGroupOf(auth, id)
		if g == "" {
			continue
		}
		v := out[g]
		if v == nil {
			v = &catalogGroupVerdict{exhausted: true, ratio: 1}
			out[g] = v
		}
		v.models = append(v.models, normQuotaModel(id))
		if *m.QuotaRemaining > 0 {
			v.exhausted = false
		}
		if *m.QuotaRemaining < v.ratio {
			v.ratio = *m.QuotaRemaining
		}
		if t, err := time.Parse(time.RFC3339, m.QuotaResetTime); err == nil && t.After(now) && (*m.QuotaRemaining <= 0 || v.resetAt.IsZero()) && t.After(v.resetAt) {
			v.resetAt = t.UTC()
		}
	}
	return out
}

// Healthy (catalog fraction >= low threshold) credential counts per model, kept
// incrementally so the low-quota skip never empties a model.
var (
	lowHealthyMu      sync.Mutex
	lowHealthyByAuth  = map[string]map[string]bool{}
	lowHealthyByModel = map[string]int{}
)

func updateLowHealthy(authID string, healthy map[string]bool) {
	lowHealthyMu.Lock()
	defer lowHealthyMu.Unlock()
	for m := range lowHealthyByAuth[authID] {
		lowHealthyByModel[m]--
	}
	for m := range healthy {
		lowHealthyByModel[m]++
	}
	lowHealthyByAuth[authID] = healthy
}

func lowHealthyCount(model string) int {
	lowHealthyMu.Lock()
	defer lowHealthyMu.Unlock()
	return lowHealthyByModel[model]
}

// applyCatalogQuotaGroups learns groups and applies group blocks / releases from a fresh catalog.
func (r *Runtime) applyCatalogQuotaGroups(authID string, h *antigravityCatalogHealth) {
	if r == nil || r.coreManager == nil || h == nil {
		return
	}
	auth, ok := r.coreManager.GetByID(authID)
	if !ok || auth == nil || !strings.EqualFold(auth.Provider, "antigravity") {
		return
	}
	r.syncQuotaGroupRules()
	r.recordLearnedQuotaGroups(auth, h)
	now := time.Now().UTC()
	thr := r.quotaLowRemaining()
	healthy := map[string]bool{}
	for id, m := range h.models {
		if m.QuotaRemaining != nil && *m.QuotaRemaining >= thr {
			healthy[normQuotaModel(id)] = true
		}
	}
	if auth.Disabled {
		healthy = nil
	}
	updateLowHealthy(authID, healthy)
	if auth.Disabled || !r.catalogLeader() || now.Sub(h.fetchedAt) > 10*time.Minute || r.coreManager.QuotaCooldownDisabledFor(authID) {
		return
	}
	enabled := r.quotaGroupCooldownEnabled()
	interval := r.AntigravityCatalogInterval()
	if interval <= 0 {
		interval = defaultCatalogRefreshInterval
	}
	ctx := context.Background()
	blocked, released, low := 0, 0, 0
	for group, v := range catalogGroupVerdicts(auth, h, now) {
		models := credentialGroupModels(auth, group)
		switch {
		case enabled && v.exhausted:
			until := now.Add(2 * interval)
			if !v.resetAt.IsZero() && v.resetAt.Sub(now) <= quotaBucketMaxHorizon {
				until = v.resetAt
			}
			msg := quotaGroupMessage(group, v.resetAt)
			for _, m := range models {
				if st := auth.ModelStates[m]; st != nil && st.LastError != nil && st.LastError.Code == coreauth.ModelQuotaExhaustedCode && st.Unavailable {
					continue // the request path already holds this one
				}
				if r.groupBlock(ctx, auth, m, QuotaGroupExhaustedCode, msg, until) {
					blocked++
				}
			}
		default:
			for _, m := range models {
				st := auth.ModelStates[m]
				if st == nil || !st.Unavailable || !st.NextRetryAfter.After(now) || st.LastError == nil {
					continue
				}
				switch st.LastError.Code {
				case QuotaGroupExhaustedCode:
					if _, err := r.coreManager.ClearQuotaCooldown(ctx, auth.ID, m); err == nil {
						released++
					}
				case QuotaGroupLowCode:
					if v.ratio >= thr || thr <= 0 {
						if _, err := r.coreManager.ClearQuotaCooldown(ctx, auth.ID, m); err == nil {
							released++
						}
					}
				case coreauth.ModelQuotaExhaustedCode:
					// Catalog shows quota for the group again: let the prober (and canary) decide.
					if cm, ok := h.models[strings.ToLower(m)]; ok && cm.QuotaRemaining != nil && *cm.QuotaRemaining > 0 {
						r.prober.probeNow(auth.ID, m)
					}
				}
			}
			if enabled && thr > 0 && v.ratio < thr && v.ratio > 0 {
				until := now.Add(2 * interval)
				if !v.resetAt.IsZero() && v.resetAt.Before(until) {
					until = v.resetAt
				}
				msg := fmt.Sprintf("额度组 %s 剩余 %.0f%%，暂不调度", group, v.ratio*100)
				for _, m := range models {
					if lowHealthyCount(normQuotaModel(m)) < quotaGroupLowMinHealthy {
						continue // never take the last few credentials of a model away
					}
					if st := auth.ModelStates[m]; st != nil && st.Unavailable && st.NextRetryAfter.After(now) {
						continue
					}
					if r.groupBlock(ctx, auth, m, QuotaGroupLowCode, msg, until) {
						low++
					}
				}
			}
		}
	}
	quotaGroupCatalogBlocked.Add(int64(blocked))
	quotaGroupReleased.Add(int64(released))
	quotaGroupLowBlocked.Add(int64(low))
	if blocked+released+low > 0 {
		log.Infof("quota group (catalog): %s: %d blocked, %d released, %d low-skipped", auth.ID, blocked, released, low)
	}
}

// CredentialQuotaGroup is one group of a credential for the panel.
type CredentialQuotaGroup struct {
	Group     string   `json:"group"`
	Source    string   `json:"source"`
	Models    []string `json:"models"`
	Remaining *float64 `json:"remaining,omitempty"`
	ResetAt   string   `json:"reset_at,omitempty"`
	Cooling   int      `json:"cooling"`
}

// CredentialQuotaGroups lists the resolved quota groups of one credential.
func (r *Runtime) CredentialQuotaGroups(authID string) []CredentialQuotaGroup {
	if r == nil || r.coreManager == nil {
		return nil
	}
	auth, ok := r.coreManager.GetByID(authID)
	if !ok || auth == nil {
		return nil
	}
	r.syncQuotaGroupRules()
	antigravityCatalogMu.Lock()
	var h *antigravityCatalogHealth
	if e := antigravityCatalog[authID]; e != nil {
		h = e.health
	}
	antigravityCatalogMu.Unlock()
	now := time.Now()
	idx := map[string]*CredentialQuotaGroup{}
	var order []string
	for _, m := range credentialModels(auth) {
		g, src := QuotaGroupFor(auth, m)
		if g == "" {
			continue
		}
		cg := idx[g]
		if cg == nil {
			cg = &CredentialQuotaGroup{Group: g, Source: src}
			idx[g] = cg
			order = append(order, g)
		}
		cg.Models = append(cg.Models, m)
		if st := auth.ModelStates[m]; st != nil && st.Unavailable && st.NextRetryAfter.After(now) {
			cg.Cooling++
		}
		if h != nil {
			if cm, ok := h.models[strings.ToLower(m)]; ok && cm.QuotaRemaining != nil {
				if cg.Remaining == nil || *cm.QuotaRemaining < *cg.Remaining {
					v := *cm.QuotaRemaining
					cg.Remaining = &v
					cg.ResetAt = cm.QuotaResetTime
				}
			}
		}
	}
	sort.Strings(order)
	out := make([]CredentialQuotaGroup, 0, len(order))
	for _, g := range order {
		out = append(out, *idx[g])
	}
	return out
}

// filterQuotaProbePairs keeps one representative per (credential, quota group) (pairs are
// sorted nearest reset first) and drops pairs whose known reset is further than skipFar
// unless explicitly nudged. Caller holds the prober lock (nudged is read only).
func filterQuotaProbePairs(pairs []quotaProbeTarget, nudged map[string]bool, skipFar time.Duration, now time.Time) []quotaProbeTarget {
	seen := map[string]bool{}
	out := pairs[:0:0]
	for _, p := range pairs {
		key := p.auth.ID + "|" + p.model
		if skipFar > 0 && p.reset.Sub(now) > skipFar && !nudged[key] {
			quotaProbeSkipped.Add(1)
			continue
		}
		if g := quotaGroupOf(p.auth, p.model); g != "" {
			gk := p.auth.ID + "|" + g
			if seen[gk] && !nudged[key] {
				quotaProbeGroupDeduped.Add(1)
				continue
			}
			seen[gk] = true
		}
		out = append(out, p)
	}
	return out
}

// releaseQuotaGroupSiblings clears propagated group cooldowns after the representative recovered.
func (r *Runtime) releaseQuotaGroupSiblings(ctx context.Context, authID, model string) {
	if r == nil || r.coreManager == nil {
		return
	}
	auth, ok := r.coreManager.GetByID(authID)
	if !ok || auth == nil {
		return
	}
	group := quotaGroupOf(auth, model)
	now := time.Now()
	for _, m := range credentialGroupModels(auth, group) {
		st := auth.ModelStates[m]
		if m == model || st == nil || !st.Unavailable || !st.NextRetryAfter.After(now) || st.LastError == nil {
			continue
		}
		if st.LastError.Code == QuotaGroupExhaustedCode {
			if _, err := r.coreManager.ClearQuotaCooldown(ctx, authID, m); err == nil {
				quotaGroupReleased.Add(1)
			}
		}
	}
}
