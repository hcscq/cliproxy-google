package auth

// Cluster-wide (credential, model-family) health.
//
// Every usage record observed by any Home replica updates one Redis hash field
// (atomically, via Lua), so slow-event thresholds, quarantine escalation and
// 503 capacity escalation are shared by all proxy pods and Home replicas.
// Each replica keeps a bounded local mirror (refreshed every few seconds) that
// the scheduler reads for soft weighting: degraded pairs get proportionally
// fewer picks, never zero (probe floor), and never affect other families on
// the same credential.

import (
	"context"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
)

// HealthSettings tunes the cluster health tracker.
type HealthSettings struct {
	Alpha            float64         // EWMA factor
	SlowEvents       int             // slow events within Window before quarantine
	Window           time.Duration   // slow-event window
	Quarantine       time.Duration   // first quarantine, doubles per level
	QuarantineMax    time.Duration   // quarantine cap
	SubThresholdCool time.Duration   // cooldown for a slow event below threshold
	CapacitySteps    []time.Duration // 503 no-capacity escalation
	SlowClientWait   time.Duration   // 499 after this long counts as slow
	MinSamples       int             // samples before soft weighting applies
	SlowFactor       float64         // TTFB above family baseline * factor degrades weight
	ProbeFloor       float64         // minimum pick weight (probe traffic)
	Idle             time.Duration   // drop pairs idle this long
	MaxEntries       int             // local mirror bound
	SyncInterval     time.Duration
}

// DefaultHealthSettings returns the defaults.
func DefaultHealthSettings() HealthSettings {
	return HealthSettings{
		Alpha: 0.2, SlowEvents: 2, Window: 10 * time.Minute,
		Quarantine: 5 * time.Minute, QuarantineMax: 15 * time.Minute, SubThresholdCool: 15 * time.Second,
		CapacitySteps:  []time.Duration{time.Second, 10 * time.Second, time.Minute},
		SlowClientWait: 45 * time.Second, MinSamples: 5, SlowFactor: 1.5, ProbeFloor: 0.15,
		Idle: 30 * time.Minute, MaxEntries: 100000, SyncInterval: 10 * time.Second,
	}
}

var healthBucketRe = regexp.MustCompile(`^(gemini|claude)-(\d+(?:\.\d+)?)`)

// HealthFamily groups models that share upstream degradation (all gemini-3.8-* tiers).
func HealthFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(CanonicalModelID(model)))
	if m == "" {
		m = strings.ToLower(strings.TrimSpace(model))
	}
	if g := healthBucketRe.FindStringSubmatch(m); g != nil {
		return g[1] + "-" + g[2]
	}
	return m
}

// HealthLabel is the short UI label of a family ("3.8").
func HealthLabel(family string) string {
	if g := healthBucketRe.FindStringSubmatch(family); g != nil && g[1] == "gemini" {
		return g[2]
	}
	return family
}

type healthEntry struct {
	ttfb, fail     float64
	n              int64
	updated        int64 // unix seconds
	slowCount      int64
	slowWin        int64
	qLevel, cLevel int64
}

func (e healthEntry) encode() string {
	return strconv.FormatFloat(e.ttfb, 'f', 1, 64) + "," + strconv.FormatFloat(e.fail, 'f', 4, 64) + "," +
		strconv.FormatInt(e.n, 10) + "," + strconv.FormatInt(e.updated, 10) + "," + strconv.FormatInt(e.slowCount, 10) + "," +
		strconv.FormatInt(e.slowWin, 10) + "," + strconv.FormatInt(e.qLevel, 10) + "," + strconv.FormatInt(e.cLevel, 10)
}

func decodeHealthEntry(s string) (healthEntry, bool) {
	p := strings.Split(s, ",")
	if len(p) < 8 {
		return healthEntry{}, false
	}
	f := func(i int) float64 { v, _ := strconv.ParseFloat(p[i], 64); return v }
	n := func(i int) int64 { v, _ := strconv.ParseFloat(p[i], 64); return int64(v) }
	return healthEntry{ttfb: f(0), fail: f(1), n: n(2), updated: n(3), slowCount: n(4), slowWin: n(5), qLevel: n(6), cLevel: n(7)}, true
}

// HealthObservation is one finished upstream attempt.
type HealthObservation struct {
	AuthID, Model string
	TTFBMs        int64
	Failed        bool // counts against the pair (5xx / timeout / slow 499)
	Slow          bool // TTFB timeout or client gone after a long wait
	Capacity      bool // 503 no capacity
	Success       bool
}

// healthLua applies one observation atomically and returns {encoded, cooldown_ms}.
const healthLua = `
local v = redis.call('HGET', KEYS[1], ARGV[1])
local now=tonumber(ARGV[2]); local ttfb=tonumber(ARGV[3]); local failed=tonumber(ARGV[4])
local slow=tonumber(ARGV[5]); local cap=tonumber(ARGV[6]); local ok=tonumber(ARGV[7])
local a=tonumber(ARGV[8]); local se=tonumber(ARGV[9]); local win=tonumber(ARGV[10])
local q=tonumber(ARGV[11]); local qmax=tonumber(ARGV[12]); local sub=tonumber(ARGV[13])
local et,ef,n,ts,sc,sw,ql,cl = 0,0,0,0,0,0,0,0
if v then
  local t={}
  for x in string.gmatch(v,'[^,]+') do t[#t+1]=tonumber(x) end
  et,ef,n,ts,sc,sw,ql,cl = t[1] or 0,t[2] or 0,t[3] or 0,t[4] or 0,t[5] or 0,t[6] or 0,t[7] or 0,t[8] or 0
end
if ttfb>0 then if et<=0 then et=ttfb else et=et+a*(ttfb-et) end end
if failed==1 or ok==1 then ef=ef+a*(failed-ef); n=n+1 end
local cd=0
if slow==1 then
  if now-sw>win then sc=0; sw=now end
  sc=sc+1
  if sc>=se then
    cd=q*(2^ql); if cd>qmax then cd=qmax end
    ql=ql+1; sc=0
  else
    cd=sub
  end
elseif cap==1 then
  local steps={}
  for i=14,#ARGV do steps[#steps+1]=tonumber(ARGV[i]) end
  if #steps>0 then
    local idx=cl+1; if idx>#steps then idx=#steps end
    cd=steps[idx]
  end
  cl=cl+1
elseif ok==1 then
  ql=0; cl=0; sc=0
end
local s=string.format('%.1f,%.4f,%d,%d,%d,%d,%d,%d', et, ef, n, now, sc, sw, ql, cl)
redis.call('HSET', KEYS[1], ARGV[1], s)
return {s, math.floor(cd)}
`

// HealthTracker is the cluster-wide tracker; the zero value is unusable, use NewHealthTracker.
type HealthTracker struct {
	settings HealthSettings
	mu       sync.RWMutex
	entries  map[string]healthEntry // key: authID|family
	baseline map[string]float64     // family → typical TTFB (median of pair EWMAs)
	byAuth   map[string][]string    // authID → families present (UI lookup)
	script   *redis.Script
	rc       func() *redisx.Client
	key      string
}

// NewHealthTracker builds a tracker; rc returns the shared Redis (nil = local only).
func NewHealthTracker(s HealthSettings, rc func() *redisx.Client) *HealthTracker {
	t := &HealthTracker{settings: s, entries: map[string]healthEntry{}, baseline: map[string]float64{}, byAuth: map[string][]string{}, script: redis.NewScript(healthLua), rc: rc}
	return t
}

func healthKey(authID, family string) string { return authID + "|" + family }

func (t *HealthTracker) redis() *redisx.Client {
	if t == nil || t.rc == nil {
		return nil
	}
	c := t.rc()
	if c == nil || !c.Healthy() {
		return nil
	}
	return c
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Observe records one attempt and returns the cooldown to apply to the
// (credential, model) pair: quarantine for slow events, an escalating step for
// capacity, 0 otherwise.
func (t *HealthTracker) Observe(ctx context.Context, o HealthObservation) time.Duration {
	if t == nil || o.AuthID == "" {
		return 0
	}
	fam := HealthFamily(o.Model)
	if fam == "" {
		return 0
	}
	key := healthKey(o.AuthID, fam)
	now := time.Now().Unix()
	s := t.settings
	if c := t.redis(); c != nil {
		args := []any{key, now, o.TTFBMs, b2i(o.Failed), b2i(o.Slow), b2i(o.Capacity), b2i(o.Success), s.Alpha, s.SlowEvents,
			int64(s.Window / time.Second), s.Quarantine.Milliseconds(), s.QuarantineMax.Milliseconds(), s.SubThresholdCool.Milliseconds()}
		for _, st := range s.CapacitySteps {
			args = append(args, st.Milliseconds())
		}
		rctx, cancel := c.Ctx(ctx)
		res, err := t.script.Run(rctx, c.Raw(), []string{c.Key("health", "pairs")}, args...).Slice()
		cancel()
		c.Observe(err)
		if err == nil && len(res) == 2 {
			enc, _ := res[0].(string)
			cd, _ := res[1].(int64)
			if e, ok := decodeHealthEntry(enc); ok {
				t.store(key, e)
			}
			return time.Duration(cd) * time.Millisecond
		}
		if err != nil {
			log.Debugf("health: redis observe failed, using local: %v", err)
		}
	}
	return t.observeLocal(key, now, o)
}

func (t *HealthTracker) observeLocal(key string, now int64, o HealthObservation) time.Duration {
	s := t.settings
	t.mu.Lock()
	defer t.mu.Unlock()
	t.indexLocked(key)
	e := t.entries[key]
	if o.TTFBMs > 0 {
		if e.ttfb <= 0 {
			e.ttfb = float64(o.TTFBMs)
		} else {
			e.ttfb += s.Alpha * (float64(o.TTFBMs) - e.ttfb)
		}
	}
	if o.Failed || o.Success {
		e.fail += s.Alpha * (float64(b2i(o.Failed)) - e.fail)
		e.n++
	}
	var cd time.Duration
	switch {
	case o.Slow:
		if now-e.slowWin > int64(s.Window/time.Second) {
			e.slowCount, e.slowWin = 0, now
		}
		e.slowCount++
		if e.slowCount >= int64(s.SlowEvents) {
			cd = s.Quarantine << uint(e.qLevel)
			if cd > s.QuarantineMax || cd <= 0 {
				cd = s.QuarantineMax
			}
			e.qLevel++
			e.slowCount = 0
		} else {
			cd = s.SubThresholdCool
		}
	case o.Capacity:
		if n := len(s.CapacitySteps); n > 0 {
			i := int(e.cLevel)
			if i >= n {
				i = n - 1
			}
			cd = s.CapacitySteps[i]
		}
		e.cLevel++
	case o.Success:
		e.qLevel, e.cLevel, e.slowCount = 0, 0, 0
	}
	e.updated = now
	t.entries[key] = e
	t.boundLocked()
	return cd
}

func (t *HealthTracker) store(key string, e healthEntry) {
	t.mu.Lock()
	t.indexLocked(key)
	t.entries[key] = e
	t.boundLocked()
	t.mu.Unlock()
}

func (t *HealthTracker) indexLocked(key string) {
	if _, ok := t.entries[key]; ok {
		return
	}
	i := strings.LastIndexByte(key, '|')
	if i < 0 {
		return
	}
	t.byAuth[key[:i]] = append(t.byAuth[key[:i]], key[i+1:])
}

func (t *HealthTracker) rebuildIndexLocked() {
	t.byAuth = make(map[string][]string, len(t.entries))
	for k := range t.entries {
		if i := strings.LastIndexByte(k, '|'); i >= 0 {
			t.byAuth[k[:i]] = append(t.byAuth[k[:i]], k[i+1:])
		}
	}
}

// boundLocked evicts the stalest entries above MaxEntries.
func (t *HealthTracker) boundLocked() {
	over := len(t.entries) - t.settings.MaxEntries
	if over <= 0 {
		return
	}
	type kv struct {
		k string
		u int64
	}
	all := make([]kv, 0, len(t.entries))
	for k, e := range t.entries {
		all = append(all, kv{k, e.updated})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].u < all[j].u })
	// evict 10% extra so this does not run on every insert
	over += t.settings.MaxEntries / 10
	for i := 0; i < over && i < len(all); i++ {
		delete(t.entries, all[i].k)
	}
	t.rebuildIndexLocked()
}

// Weight returns the pick weight in [ProbeFloor, 1] for a pair.
func (t *HealthTracker) Weight(authID, model string) float64 {
	if t == nil {
		return 1
	}
	fam := HealthFamily(model)
	t.mu.RLock()
	e, ok := t.entries[healthKey(authID, fam)]
	base := t.baseline[fam]
	t.mu.RUnlock()
	if !ok {
		return 1
	}
	return t.weightOf(e, base)
}

func (t *HealthTracker) weightOf(e healthEntry, base float64) float64 {
	s := t.settings
	if e.n < int64(s.MinSamples) || time.Since(time.Unix(e.updated, 0)) > s.Idle {
		return 1
	}
	w := 1 - e.fail
	if base > 0 && e.ttfb > base*s.SlowFactor {
		w *= base * s.SlowFactor / e.ttfb
	}
	if w < s.ProbeFloor {
		w = s.ProbeFloor
	}
	if w > 1 {
		w = 1
	}
	return w
}

// Admit decides probabilistically whether a pick may use this pair.
func (t *HealthTracker) Admit(authID, model string) bool {
	w := t.Weight(authID, model)
	return w >= 1 || rand.Float64() < w
}

// HealthStatus is the concise per-family state of one credential for the UI.
type HealthStatus struct {
	Family string  `json:"family"`
	Label  string  `json:"label"`
	State  string  `json:"state"` // 慢 / 降级
	Weight float64 `json:"weight"`
	TTFBMs int64   `json:"ttfb_ms"`
	Fail   float64 `json:"fail_ratio"`
}

// Status lists the degraded families of a credential (empty when healthy).
func (t *HealthTracker) Status(authID string) []HealthStatus {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []HealthStatus
	for _, fam := range t.byAuth[authID] {
		e, ok := t.entries[healthKey(authID, fam)]
		if !ok {
			continue
		}
		w := t.weightOf(e, t.baseline[fam])
		if w >= 0.9 && e.qLevel == 0 {
			continue
		}
		st := "慢"
		if w < 0.5 || e.qLevel > 0 {
			st = "降级"
		}
		out = append(out, HealthStatus{Family: fam, Label: HealthLabel(fam), State: st, Weight: w, TTFBMs: int64(e.ttfb), Fail: e.fail})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// Run refreshes the local mirror from Redis and prunes idle pairs until ctx ends.
func (t *HealthTracker) Run(ctx context.Context) {
	tick := time.NewTicker(t.settings.SyncInterval)
	defer tick.Stop()
	for {
		t.sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (t *HealthTracker) sync(ctx context.Context) {
	now := time.Now().Unix()
	idle := int64(t.settings.Idle / time.Second)
	next := map[string]healthEntry{}
	if c := t.redis(); c != nil {
		key := c.Key("health", "pairs")
		var cursor uint64
		var stale []string
		for {
			rctx, cancel := c.Ctx(ctx)
			kvs, nc, err := c.Raw().HScan(rctx, key, cursor, "", 1000).Result()
			cancel()
			c.Observe(err)
			if err != nil {
				return // keep the current mirror
			}
			for i := 0; i+1 < len(kvs); i += 2 {
				e, ok := decodeHealthEntry(kvs[i+1])
				if !ok || now-e.updated > idle {
					stale = append(stale, kvs[i])
					continue
				}
				next[kvs[i]] = e
			}
			cursor = nc
			if cursor == 0 || len(next) > t.settings.MaxEntries {
				break
			}
		}
		if len(stale) > 0 {
			rctx, cancel := c.Ctx(ctx)
			_ = c.Raw().HDel(rctx, key, stale...).Err()
			cancel()
		}
	} else {
		t.mu.RLock()
		for k, e := range t.entries {
			if now-e.updated <= idle {
				next[k] = e
			}
		}
		t.mu.RUnlock()
	}
	base := familyBaselines(next, t.settings.MinSamples)
	t.mu.Lock()
	t.entries = next
	t.baseline = base
	t.rebuildIndexLocked()
	t.boundLocked()
	t.mu.Unlock()
}

// familyBaselines is the median TTFB EWMA over sampled pairs per family.
func familyBaselines(entries map[string]healthEntry, minSamples int) map[string]float64 {
	per := map[string][]float64{}
	for k, e := range entries {
		if e.n < int64(minSamples) || e.ttfb <= 0 {
			continue
		}
		if i := strings.LastIndexByte(k, '|'); i >= 0 {
			per[k[i+1:]] = append(per[k[i+1:]], e.ttfb)
		}
	}
	out := make(map[string]float64, len(per))
	for f, v := range per {
		sort.Float64s(v)
		out[f] = v[len(v)/2]
	}
	return out
}

var (
	defaultHealthOnce sync.Once
	defaultHealth     *HealthTracker
)

// ClusterHealth is the process-wide tracker backed by the default Redis client.
func ClusterHealth() *HealthTracker {
	defaultHealthOnce.Do(func() {
		defaultHealth = NewHealthTracker(DefaultHealthSettings(), redisx.Default)
	})
	return defaultHealth
}
