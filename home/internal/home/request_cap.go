package home

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
)

// Per-credential request caps (hourly/daily), counted in Redis per fixed window
// (hour / day in UTC+8). Only credentials with a cap pay any cost: dispatch
// enqueues one counter event; a worker pipelines INCRs; a 10s sync re-reads
// counters (other Homes' increments, cap edits, window rollover). The scheduler
// only checks an in-process capped set. Redis down = fail open.

const (
	RequestCapModeKey   = "request_cap_mode" // "" inherit tier | custom | unlimited
	RequestCapHourlyKey = "request_cap_hourly"
	RequestCapDailyKey  = "request_cap_daily"

	reqCapSyncEvery = 10 * time.Second
	reqCapQueue     = 65536
)

var reqCapZone = time.FixedZone("UTC+8", 8*3600)

type reqCapLimits struct{ Hourly, Daily int64 }

// RequestCapUsage is what the panel shows for one credential.
type RequestCapUsage struct {
	Mode        string    `json:"mode"`
	Tier        string    `json:"tier"`
	HourlyCap   int64     `json:"hourly_cap"`
	DailyCap    int64     `json:"daily_cap"`
	HourlyUsed  int64     `json:"hourly_used"`
	DailyUsed   int64     `json:"daily_used"`
	CappedUntil time.Time `json:"capped_until,omitzero"`
}

type requestCapGate struct {
	r      *Runtime
	limits atomic.Value // map[string]reqCapLimits
	modes  atomic.Value // map[string][2]string (mode, tier)
	events chan string
	once   sync.Once
}

func reqCapWindows(now time.Time) (hourKey, dayKey string, hourEnd, dayEnd time.Time) {
	t := now.In(reqCapZone)
	h := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, reqCapZone)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, reqCapZone)
	return h.Format("2006010215"), d.Format("20060102"), h.Add(time.Hour), d.AddDate(0, 0, 1)
}

func reqCapTier(a *coreauth.Auth) string {
	t := strings.ToLower(stringMeta(a, "subscription_tier"))
	switch {
	case strings.Contains(t, "ultra"):
		return "ultra"
	case strings.Contains(t, "pro"):
		return "pro"
	case t != "":
		return "free"
	}
	return "unknown"
}

func stringMeta(a *coreauth.Auth, key string) string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	switch v := a.Metadata[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case interface{ String() string }:
		return v.String()
	}
	return ""
}

func intMeta(a *coreauth.Auth, key string) int64 {
	n, _ := strconv.ParseInt(stringMeta(a, key), 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

// effectiveRequestCap resolves (mode, tier, limits) for a credential.
func (r *Runtime) effectiveRequestCap(a *coreauth.Auth) (string, string, reqCapLimits) {
	mode := strings.ToLower(stringMeta(a, RequestCapModeKey))
	tier := reqCapTier(a)
	switch mode {
	case "unlimited":
		return mode, tier, reqCapLimits{}
	case "custom":
		return mode, tier, reqCapLimits{Hourly: intMeta(a, RequestCapHourlyKey), Daily: intMeta(a, RequestCapDailyKey)}
	}
	var l reqCapLimits
	if cfg := r.Config(); cfg != nil {
		h := cfg.Antigravity.Health
		switch tier {
		case "free":
			l = reqCapLimits{int64(h.RequestCapHourlyFree), int64(h.RequestCapDailyFree)}
		case "pro":
			l = reqCapLimits{int64(h.RequestCapHourlyPro), int64(h.RequestCapDailyPro)}
		case "ultra":
			l = reqCapLimits{int64(h.RequestCapHourlyUltra), int64(h.RequestCapDailyUltra)}
		default:
			l = reqCapLimits{int64(h.RequestCapHourlyUnknown), int64(h.RequestCapDailyUnknown)}
		}
	}
	if l.Hourly < 0 {
		l.Hourly = 0
	}
	if l.Daily < 0 {
		l.Daily = 0
	}
	return "inherit", tier, l
}

func (g *requestCapGate) limitsFor(id string) (reqCapLimits, bool) {
	m, _ := g.limits.Load().(map[string]reqCapLimits)
	l, ok := m[id]
	return l, ok
}

// OnDispatch implements coreauth.RequestCapGate.
func (g *requestCapGate) OnDispatch(authID string) {
	if _, ok := g.limitsFor(authID); !ok {
		return
	}
	select {
	case g.events <- authID:
	default: // queue full: drop the count rather than block dispatch
	}
}

func (g *requestCapGate) key(rc *redisx.Client, kind, id, win string) string {
	return rc.Key("reqcap", kind, id, win)
}

func (g *requestCapGate) worker(ctx context.Context) {
	batch := make([]string, 0, 512)
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-g.events:
			batch = append(batch[:0], id)
		drain:
			for len(batch) < 512 {
				select {
				case id := <-g.events:
					batch = append(batch, id)
				default:
					break drain
				}
			}
			g.flush(ctx, batch)
		}
	}
}

func (g *requestCapGate) flush(ctx context.Context, ids []string) {
	rc := redisx.Default()
	if !rc.Healthy() {
		return
	}
	hk, dk, hEnd, dEnd := reqCapWindows(time.Now())
	cctx, cancel := rc.Ctx(ctx)
	defer cancel()
	pipe := rc.Raw().Pipeline()
	type pair struct {
		id   string
		h, d interface{ Val() int64 }
	}
	pairs := make([]pair, 0, len(ids))
	for _, id := range ids {
		kh, kd := g.key(rc, "h", id, hk), g.key(rc, "d", id, dk)
		p := pair{id: id, h: pipe.IncrBy(cctx, kh, 1), d: pipe.IncrBy(cctx, kd, 1)}
		pipe.ExpireAt(cctx, kh, hEnd.Add(10*time.Minute))
		pipe.ExpireAt(cctx, kd, dEnd.Add(time.Hour))
		pairs = append(pairs, p)
	}
	_, err := pipe.Exec(cctx)
	rc.Observe(err)
	if err != nil {
		return
	}
	for _, p := range pairs {
		g.evaluate(p.id, p.h.Val(), p.d.Val(), hEnd, dEnd)
	}
}

func (g *requestCapGate) evaluate(id string, hUsed, dUsed int64, hEnd, dEnd time.Time) {
	l, ok := g.limitsFor(id)
	if !ok {
		coreauth.MarkRequestCapped(id, time.Time{})
		return
	}
	var until time.Time
	if l.Daily > 0 && dUsed >= l.Daily {
		until = dEnd
	} else if l.Hourly > 0 && hUsed >= l.Hourly {
		until = hEnd
	}
	prev, was := coreauth.RequestCappedUntil(id)
	coreauth.MarkRequestCapped(id, until)
	if !until.IsZero() && (!was || !prev.Equal(until)) {
		log.WithFields(log.Fields{"auth": id, "hourly": hUsed, "daily": dUsed, "until": until}).Info("credential reached request cap")
	}
}

// sync rebuilds the limits map and re-reads all counters for capped credentials.
func (g *requestCapGate) sync(ctx context.Context) {
	r := g.r
	if r == nil || r.coreManager == nil {
		return
	}
	auths := r.coreManager.ListLight(func(a *coreauth.Auth) bool { return a != nil && !a.Disabled })
	limits := make(map[string]reqCapLimits)
	modes := make(map[string][2]string, len(auths))
	for _, a := range auths {
		mode, tier, l := r.effectiveRequestCap(a)
		modes[a.ID] = [2]string{mode, tier}
		if l.Hourly > 0 || l.Daily > 0 {
			limits[a.ID] = l
		}
	}
	old, _ := g.limits.Load().(map[string]reqCapLimits)
	g.limits.Store(limits)
	g.modes.Store(modes)
	for id := range old {
		if _, ok := limits[id]; !ok {
			coreauth.MarkRequestCapped(id, time.Time{})
		}
	}
	if len(limits) == 0 {
		return
	}
	ids := make([]string, 0, len(limits))
	for id := range limits {
		ids = append(ids, id)
	}
	usage := g.read(ctx, ids)
	if usage == nil {
		return
	}
	_, _, hEnd, dEnd := reqCapWindows(time.Now())
	for i, id := range ids {
		g.evaluate(id, usage[i][0], usage[i][1], hEnd, dEnd)
	}
}

// read returns [hourly, daily] counts per id (nil when Redis is unavailable).
func (g *requestCapGate) read(ctx context.Context, ids []string) [][2]int64 {
	rc := redisx.Default()
	if !rc.Healthy() || len(ids) == 0 {
		return nil
	}
	hk, dk, _, _ := reqCapWindows(time.Now())
	out := make([][2]int64, len(ids))
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		keys := make([]string, 0, 2*(end-start))
		for _, id := range ids[start:end] {
			keys = append(keys, g.key(rc, "h", id, hk), g.key(rc, "d", id, dk))
		}
		cctx, cancel := rc.Ctx(ctx)
		vals, err := rc.Raw().MGet(cctx, keys...).Result()
		cancel()
		rc.Observe(err)
		if err != nil {
			return nil
		}
		for i := range ids[start:end] {
			for j := 0; j < 2; j++ {
				if s, ok := vals[2*i+j].(string); ok {
					out[start+i][j], _ = strconv.ParseInt(s, 10, 64)
				}
			}
		}
	}
	return out
}

func (r *Runtime) startRequestCaps(ctx context.Context) {
	g := &requestCapGate{r: r, events: make(chan string, reqCapQueue)}
	g.limits.Store(map[string]reqCapLimits{})
	g.modes.Store(map[string][2]string{})
	r.reqCap = g
	coreauth.SetRequestCapGate(g)
	go g.worker(ctx)
	go func() {
		g.sync(ctx)
		t := time.NewTicker(reqCapSyncEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.sync(ctx)
			}
		}
	}()
}

// RequestCapUsage returns usage vs cap for the given credentials (panel).
func (r *Runtime) RequestCapUsage(ctx context.Context, auths []*coreauth.Auth) map[string]RequestCapUsage {
	out := make(map[string]RequestCapUsage, len(auths))
	ids := make([]string, 0, len(auths))
	for _, a := range auths {
		if a == nil {
			continue
		}
		mode, tier, l := r.effectiveRequestCap(a)
		u := RequestCapUsage{Mode: mode, Tier: tier, HourlyCap: l.Hourly, DailyCap: l.Daily}
		if until, ok := coreauth.RequestCappedUntil(a.ID); ok {
			u.CappedUntil = until
		}
		out[a.ID] = u
		ids = append(ids, a.ID)
	}
	if r.reqCap != nil {
		if usage := r.reqCap.read(ctx, ids); usage != nil {
			for i, id := range ids {
				u := out[id]
				u.HourlyUsed, u.DailyUsed = usage[i][0], usage[i][1]
				out[id] = u
			}
		}
	}
	return out
}

// ResyncRequestCaps applies cap edits immediately (instead of within 10s).
func (r *Runtime) ResyncRequestCaps(ctx context.Context) {
	if r != nil && r.reqCap != nil {
		r.reqCap.sync(ctx)
	}
}
