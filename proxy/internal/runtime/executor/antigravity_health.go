package executor

import (
	"container/list"
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"regexp"
	"strconv"
	"sync/atomic"

	"fmt"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Per-credential+model health tracking (per proxy pod, bounded LRU). Penalties
// are enforced through ordinary cooldowns: the error returned for an attempt
// carries RetryAfter (local conductor) and error.cpa_cooldown_ms in its body
// (Home derives the shared cooldown from it), so the scheduler skips the
// credential for that model. When the cooldown ends the credential gets normal
// traffic again (the probe); a repeat failure quarantines it longer.

const (
	antigravityDefaultStallCooldown = 45 * time.Second
	antigravityHealthMaxEntries     = 50000 // ~10k credentials x a few models
	antigravityHealthWindow         = 10 * time.Minute
)

type antigravityHealthSettings struct {
	ttfb, nonStream, slow time.Duration
	byFamily              map[string]time.Duration // streaming TTFB per health bucket
	nonStreamByFamily     map[string]time.Duration // non-stream attempt limit per health bucket
	quarantine, maxQ      time.Duration
	slowEvents            int
	capacitySteps         []time.Duration
	ttfbExtraPer10k       time.Duration   // first-byte allowance per 10k input tokens
	ttfbScaledMax         time.Duration   // cap for the input-scaled family limit
	stallExempt           map[string]bool // health buckets whose slow first byte is not a stall
}

// defaultAntigravityStallExemptFamilies: Gemini 4 (argon) sends nothing until
// the whole answer is generated, so a long first byte is normal there.
const defaultAntigravityStallExemptFamilies = "4"

// parseAntigravityStallExempt parses "4,3.8" into health buckets.
func parseAntigravityStallExempt(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultAntigravityStallExemptFamilies
	}
	m := map[string]bool{}
	if l := strings.ToLower(raw); l == "off" || l == "none" || l == "0" {
		return m
	}
	for _, k := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" && k[0] >= '0' && k[0] <= '9' {
			k = "gemini-" + k
		}
		if k != "" {
			m[antigravityHealthBucket(k)] = true
		}
	}
	return m
}

// stallExemptModel reports whether model's family is stall-exempt.
func (s antigravityHealthSettings) stallExemptModel(model string) bool {
	return s.stallExempt[antigravityHealthBucket(model)]
}

// antigravityHealthLive is the latest hot-reloaded health config; it wins
// over the executor's construction-time config so panel edits apply live.
var antigravityHealthLive atomic.Pointer[config.AntigravityHealthConfig]

// antigravityRequestDeadlineMargin is added on top of a lengthened family
// attempt limit for the request-wide first-byte deadline (dispatch, Home calls).
const antigravityRequestDeadlineMargin = 30 * time.Second

func init() {
	middleware.SetModelDeadlineFloor(antigravityRequestDeadlineFloor)
}

// antigravityRequestDeadlineFloor is the minimum request first-byte deadline
// for model: a family attempt limit lengthened above the base plus a margin,
// else 0 (the global deadline applies).
func antigravityRequestDeadlineFloor(model string, stream bool) time.Duration {
	if strings.TrimSpace(model) == "" {
		return 0
	}
	s := antigravityHealthSettingsFrom(nil)
	byFamily, base := s.byFamily, s.ttfb
	if !stream {
		byFamily, base = s.nonStreamByFamily, s.nonStream
	}
	fam, ok := byFamily[antigravityHealthBucket(model)]
	if !ok || fam <= 0 || (base > 0 && fam <= base) {
		return 0
	}
	return fam + antigravityRequestDeadlineMargin
}

// SetAntigravityHealthConfig installs the live health config (on load/reload).
func SetAntigravityHealthConfig(h config.AntigravityHealthConfig) {
	antigravityHealthLive.Store(&h)
	thinking.SetClampUnsupportedLevels(h.ClampThinkingLevel == nil || *h.ClampThinkingLevel)
	maxTTFB := defaultAntigravityTTFBMaxRetries
	if h.TTFBTimeoutMaxRetries != nil {
		maxTTFB = *h.TTFBTimeoutMaxRetries
	}
	cliproxyauth.SetMaxFirstByteTimeouts(maxTTFB)
	cliproxyauth.SetUnavailableModels(h.UnavailableModels)
}

// defaultAntigravityTTFBByFamily: 3.8 (tiered/high/medium/low) gets 15s; a
// stall cools the credential+model down so the retry lands elsewhere.
const defaultAntigravityTTFBByFamily = "3.8=15,4=240"

const (
	defaultAntigravityTTFBExtraPer10k = 5 * time.Second
	defaultAntigravityTTFBScaledMax   = 45 * time.Second
	defaultAntigravityTTFBMaxRetries  = 2
)

// defaultAntigravityNonStreamByFamily bounds a whole non-streaming 3.8
// attempt (no first byte exists there). 150s sits above all but ~0.1% of
// successful 3.8 non-stream responses, so only hung attempts are cut and the
// request (240s non-stream deadline) still has time for one retry.
const defaultAntigravityNonStreamByFamily = "3.8=150,4=300"

type familyTTFBCache struct {
	raw string
	m   map[string]time.Duration
}

var antigravityFamilyTTFB, antigravityFamilyNonStream atomic.Pointer[familyTTFBCache]

// parseAntigravityTTFBByFamily parses "3.8=28,2.5=60" (cached by raw string).
func parseAntigravityTTFBByFamily(raw string) map[string]time.Duration {
	return parseAntigravityFamilyDurations(raw, defaultAntigravityTTFBByFamily, &antigravityFamilyTTFB)
}

// parseAntigravityNonStreamByFamily parses the non-stream per-family limits.
func parseAntigravityNonStreamByFamily(raw string) map[string]time.Duration {
	return parseAntigravityFamilyDurations(raw, defaultAntigravityNonStreamByFamily, &antigravityFamilyNonStream)
}

func parseAntigravityFamilyDurations(raw, def string, cache *atomic.Pointer[familyTTFBCache]) map[string]time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = def
	}
	if c := cache.Load(); c != nil && c.raw == raw {
		return c.m
	}
	m := map[string]time.Duration{}
	if l := strings.ToLower(raw); l != "off" && l != "none" && l != "0" {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" && k[0] >= '0' && k[0] <= '9' {
				k = "gemini-" + k
			}
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && k != "" {
				m[antigravityHealthBucket(k)] = time.Duration(n) * time.Second
			}
		}
	}
	cache.Store(&familyTTFBCache{raw: raw, m: m})
	return m
}

func antigravityHealthSettingsFrom(cfg *config.Config) antigravityHealthSettings {
	s := antigravityHealthSettings{
		ttfb: 75 * time.Second, nonStream: 180 * time.Second, slow: 45 * time.Second,
		quarantine: 5 * time.Minute, maxQ: 15 * time.Minute, slowEvents: 2,
		capacitySteps: []time.Duration{time.Second, 10 * time.Second, time.Minute},
	}
	var h config.AntigravityHealthConfig
	switch live := antigravityHealthLive.Load(); {
	case live != nil:
		h = *live
	case cfg != nil:
		h = cfg.Antigravity.Health
	}
	s.byFamily = parseAntigravityTTFBByFamily(h.TTFBTimeoutByFamily)
	s.nonStreamByFamily = parseAntigravityNonStreamByFamily(h.NonStreamTimeoutByFamily)
	s.stallExempt = parseAntigravityStallExempt(h.Cluster.StallExemptFamilies)
	sec := func(v int, d *time.Duration, unit time.Duration) {
		switch {
		case v < 0:
			*d = 0
		case v > 0:
			*d = time.Duration(v) * unit
		}
	}
	sec(h.TTFBTimeoutSeconds, &s.ttfb, time.Second)
	s.ttfbExtraPer10k, s.ttfbScaledMax = defaultAntigravityTTFBExtraPer10k, defaultAntigravityTTFBScaledMax
	if h.TTFBExtraSecondsPer10kTokens != nil {
		s.ttfbExtraPer10k = time.Duration(max(*h.TTFBExtraSecondsPer10kTokens, 0)) * time.Second
	}
	if h.TTFBScaledMaxSeconds > 0 {
		s.ttfbScaledMax = time.Duration(h.TTFBScaledMaxSeconds) * time.Second
	}
	sec(h.NonStreamTimeoutSeconds, &s.nonStream, time.Second)
	sec(h.SlowThresholdSeconds, &s.slow, time.Second)
	sec(h.QuarantineMinutes, &s.quarantine, time.Minute)
	sec(h.QuarantineMaxMinutes, &s.maxQ, time.Minute)
	if h.SlowEvents > 0 {
		s.slowEvents = h.SlowEvents
	}
	if len(h.CapacityCooldownSeconds) > 0 {
		s.capacitySteps = s.capacitySteps[:0:0]
		for _, v := range h.CapacityCooldownSeconds {
			if v > 0 {
				s.capacitySteps = append(s.capacitySteps, time.Duration(v)*time.Second)
			}
		}
	}
	if s.maxQ < s.quarantine {
		s.maxQ = s.quarantine
	}
	return s
}

// streamTTFB is the first-byte limit for a streaming attempt of model: a
// per-family limit above the base always applies (slow-by-design families);
// a shorter per-family limit applies except for the last attempt
// the request budget allows (cutting it short would only turn a slow answer
// into an error), which keeps the base limit.
//
// payloadBytes (the upstream request body size, 0 = unknown) lengthens the
// per-family limit by ttfbExtraPer10k per 10k estimated input tokens
// (bytes/4), capped at ttfbScaledMax and the base limit: big prompts need
// longer prefill before the first byte.
func (s antigravityHealthSettings) streamTTFB(ctx context.Context, model string, payloadBytes int) time.Duration {
	byFamily := s.byFamily
	if extra := s.ttfbInputExtra(payloadBytes); extra > 0 {
		if fam, ok := byFamily[antigravityHealthBucket(model)]; ok && fam > 0 {
			scaled := fam + extra
			if hi := max(s.ttfbScaledMax, fam); scaled > hi {
				scaled = hi
			}
			if s.ttfb > 0 && fam <= s.ttfb && scaled > s.ttfb {
				scaled = s.ttfb // input scaling never lifts a shortened family above the base
			}
			byFamily = map[string]time.Duration{antigravityHealthBucket(model): scaled}
		}
	}
	return familyAttemptLimit(ctx, byFamily, model, s.ttfb)
}

// ttfbInputExtra is the first-byte allowance for a request body of n bytes.
func (s antigravityHealthSettings) ttfbInputExtra(n int) time.Duration {
	if n <= 0 || s.ttfbExtraPer10k <= 0 {
		return 0
	}
	tokens := int64(n) / 4
	return time.Duration(int64(s.ttfbExtraPer10k) / 10000 * tokens)
}

// nonStreamLimit is the whole-attempt limit for a non-streaming attempt
// (headers arrive only after generation): the per-family limit when shorter
// than the base, except for the last attempt the budget allows.
func (s antigravityHealthSettings) nonStreamLimit(ctx context.Context, model string) time.Duration {
	limit := familyAttemptLimit(ctx, s.nonStreamByFamily, model, s.nonStream)
	// Never run past the request's non-stream budget: the final attempt is
	// bounded by what is left of the request deadline.
	if rem, ok := cliproxyauth.RetryBudgetRemaining(ctx); ok && rem > 0 && (limit <= 0 || rem < limit) {
		limit = rem
	}
	return limit
}

func familyAttemptLimit(ctx context.Context, byFamily map[string]time.Duration, model string, base time.Duration) time.Duration {
	fam, ok := byFamily[antigravityHealthBucket(model)]
	if !ok || fam <= 0 || fam == base {
		return base
	}
	if base <= 0 || fam > base {
		return fam // lengthened (or base disabled): applies to every attempt
	}
	if cliproxyauth.RetryBudgetFinalAttempt(ctx, fam) {
		return base
	}
	return fam
}

type antigravityHealthEntry struct {
	key           string
	capacityLevel int
	slowAt        []time.Time
	quarantineLvl int
	lastPenalty   time.Time
}

type antigravityHealthTracker struct {
	mu    sync.Mutex
	ll    *list.List
	items map[string]*list.Element
	max   int
}

func newAntigravityHealthTracker(max int) *antigravityHealthTracker {
	return &antigravityHealthTracker{ll: list.New(), items: make(map[string]*list.Element), max: max}
}

var antigravityHealth = newAntigravityHealthTracker(antigravityHealthMaxEntries)

// antigravityHealthBucket groups models that share upstream degradation
// (e.g. every gemini-3.8-* tier) so health is tracked per credential+bucket,
// never per whole credential.
var antigravityHealthBucketRe = regexp.MustCompile(`^(gemini|claude)-(\d+(?:\.\d+)?)`)

func antigravityHealthBucket(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if g := antigravityHealthBucketRe.FindStringSubmatch(m); g != nil {
		return g[1] + "-" + g[2]
	}
	return m
}

func antigravityHealthLabel(model string) string {
	b := antigravityHealthBucket(model)
	if g := antigravityHealthBucketRe.FindStringSubmatch(b); g != nil && g[1] == "gemini" {
		return g[2]
	}
	return b
}

func antigravityHealthKey(authID, model string) string {
	return authID + "\x00" + antigravityHealthBucket(model)
}

func (t *antigravityHealthTracker) get(key string, create bool) *antigravityHealthEntry {
	if el, ok := t.items[key]; ok {
		t.ll.MoveToFront(el)
		return el.Value.(*antigravityHealthEntry)
	}
	if !create {
		return nil
	}
	e := &antigravityHealthEntry{key: key}
	t.items[key] = t.ll.PushFront(e)
	for t.ll.Len() > t.max {
		last := t.ll.Back()
		t.ll.Remove(last)
		delete(t.items, last.Value.(*antigravityHealthEntry).key)
	}
	return e
}

// success clears capacity escalation and slow history; the quarantine level
// decays only after a quiet window so a flapping credential keeps escalating.
func (t *antigravityHealthTracker) success(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.get(key, false)
	if e == nil {
		return
	}
	e.capacityLevel = 0
	e.slowAt = e.slowAt[:0]
	if e.quarantineLvl > 0 && now.Sub(e.lastPenalty) > antigravityHealthWindow {
		e.quarantineLvl = 0
	}
	if e.capacityLevel == 0 && e.quarantineLvl == 0 {
		t.ll.Remove(t.items[key])
		delete(t.items, key)
	}
}

// capacity returns the cooldown for another 503 "No capacity" on key.
func (t *antigravityHealthTracker) capacity(key string, s antigravityHealthSettings) time.Duration {
	if len(s.capacitySteps) == 0 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.get(key, true)
	i := e.capacityLevel
	if i >= len(s.capacitySteps) {
		i = len(s.capacitySteps) - 1
	}
	e.capacityLevel++
	return s.capacitySteps[i]
}

// slow records a slow event (TTFB timeout or long-wait client disconnect).
// It returns the quarantine to apply (0 if the threshold is not yet reached).
func (t *antigravityHealthTracker) slow(key string, now time.Time, s antigravityHealthSettings) time.Duration {
	if s.quarantine <= 0 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.get(key, true)
	kept := e.slowAt[:0]
	for _, at := range e.slowAt {
		if now.Sub(at) <= antigravityHealthWindow {
			kept = append(kept, at)
		}
	}
	e.slowAt = append(kept, now)
	if len(e.slowAt) > s.slowEvents {
		e.slowAt = e.slowAt[len(e.slowAt)-s.slowEvents:]
	}
	if len(e.slowAt) < s.slowEvents {
		return 0
	}
	d := s.quarantine << uint(e.quarantineLvl)
	if d > s.maxQ || d <= 0 {
		d = s.maxQ
	}
	e.quarantineLvl++
	e.lastPenalty = now
	e.slowAt = e.slowAt[:0]
	return d
}

func (t *antigravityHealthTracker) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ll.Len()
}

// antigravityTTFBTimeoutErr is returned when an attempt produced no first byte
// in time; it is a credential failure so the conductor switches credentials.
// antigravityTTFBTimeoutFinalErr is the first-byte timeout of a stall-exempt
// family: no cooldown (1ms hint so no default cooldown applies either) and a
// marker telling the conductor not to retry on another credential.
func antigravityTTFBTimeoutFinalErr(model string, wait time.Duration) statusErr {
	cd := time.Millisecond
	body := fmt.Sprintf(`{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"%s: no first byte from upstream within %s (slow by design; not retried)","cpa_cooldown_ms":1,"cpa_ttfb_final":true}}`, antigravityHealthLabel(model), wait.Round(time.Second))
	return statusErr{code: http.StatusGatewayTimeout, msg: body, retryAfter: &cd}
}

func antigravityTTFBTimeoutErr(model string, wait, cooldown time.Duration) statusErr {
	state := antigravityHealthLabel(model) + " 慢"
	if cooldown >= time.Minute {
		state = antigravityHealthLabel(model) + " 降级"
	}
	if cooldown <= 0 {
		cooldown = antigravityDefaultStallCooldown
	}
	body := fmt.Sprintf(`{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"%s: no first byte from upstream within %s","cpa_cooldown_ms":%d}}`, state, wait.Round(time.Second), cooldown.Milliseconds())
	return statusErr{code: http.StatusGatewayTimeout, msg: body, retryAfter: &cooldown}
}

func isAntigravityCapacityBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "no capacity available") || strings.Contains(lower, "model_capacity_exhausted")
}

// withAntigravityCooldownHint annotates an upstream error body with the
// cooldown Home should apply.
func withAntigravityCooldownHint(body []byte, d time.Duration) []byte {
	if d <= 0 || !gjson.GetBytes(body, "error").IsObject() {
		return body
	}
	out, err := sjson.SetBytes(body, "error.cpa_cooldown_ms", d.Milliseconds())
	if err != nil {
		return body
	}
	return out
}

// antigravityAttemptTimer bounds the wait for an attempt's first response.
type antigravityAttemptTimer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	timer    *time.Timer
	timedOut atomic.Bool
	limit    time.Duration
	started  time.Time
}

func newAntigravityAttemptTimer(parent context.Context, limit time.Duration) *antigravityAttemptTimer {
	t := &antigravityAttemptTimer{limit: limit, started: time.Now()}
	t.ctx, t.cancel = context.WithCancel(parent)
	if limit > 0 {
		t.timer = time.AfterFunc(limit, func() {
			t.timedOut.Store(true)
			t.cancel()
		})
	}
	return t
}

// firstByte stops the timer; it reports false if the timer already fired.
func (t *antigravityAttemptTimer) firstByte() bool {
	if t.timer != nil {
		t.timer.Stop()
	}
	return !t.timedOut.Load()
}

// penalty applies the health penalty for a failed wait and returns the
// replacement error (nil when the failure is not a slow event).
func (t *antigravityAttemptTimer) penalty(parent context.Context, auth *cliproxyauth.Auth, model string, s antigravityHealthSettings) error {
	now := time.Now()
	key := antigravityHealthKey(auth.ID, model)
	if s.stallExemptModel(model) {
		if t.timedOut.Load() {
			log.Warnf("antigravity executor: no response within %s (auth=%s model=%s), stall-exempt family: no cooldown, no retry", t.limit, auth.ID, model)
			return antigravityTTFBTimeoutFinalErr(model, t.limit)
		}
		return nil
	}
	if t.timedOut.Load() {
		cd := antigravityLocalSlow(key, now, s)
		log.Warnf("antigravity executor: no response within %s (auth=%s model=%s), switching credential; quarantine=%s", t.limit, auth.ID, model, cd)
		return antigravityTTFBTimeoutErr(model, t.limit, cd)
	}
	if parent.Err() != nil && s.slow > 0 && now.Sub(t.started) >= s.slow {
		if cd := antigravityLocalSlow(key, now, s); cd > 0 {
			_ = markAntigravityShortCooldownRequired(context.Background(), auth, model, now, cd)
		}
	}
	return nil
}

// capacityErr builds the 503 capacity error with an escalating cooldown.
func antigravityCapacityErr(auth *cliproxyauth.Auth, model string, s antigravityHealthSettings, code int, body []byte, hdr http.Header) statusErr {
	cd := antigravityHealth.capacity(antigravityHealthKey(auth.ID, model), s)
	err := newAntigravityStatusErrWithHeaders(code, withAntigravityCooldownHint(body, cd), hdr)
	if cd > 0 {
		err.retryAfter = &cd
	}
	return err
}

// antigravityLocalSlow records a slow event in the pod-local tracker. In Home
// mode, Home tracks slow events cluster-wide and decides the quarantine, so the
// pod-local tracker stays out (returns 0).
func antigravityLocalSlow(key string, now time.Time, s antigravityHealthSettings) time.Duration {
	if _, homeMode, _ := currentAntigravityKVClient(); homeMode {
		return 0
	}
	return antigravityHealth.slow(key, now, s)
}
