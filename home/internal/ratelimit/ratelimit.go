// Package ratelimit implements cluster-wide requests-per-minute limits.
//
// With Redis healthy the window lives in Redis, so the limit is global across
// Home replicas. Without Redis (disabled, down, failing over) each Home
// enforces limit/liveHomes locally: an approximation that still bounds the
// total, and never blocks dispatch on an unavailable dependency.
package ratelimit

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

// Two-bucket sliding window: estimate = prev*(1-elapsed) + cur.
var allowScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local prev = tonumber(redis.call('GET', KEYS[2]) or '0')
local est = prev * (60000 - tonumber(ARGV[2])) / 60000 + cur
if est + 1 > tonumber(ARGV[1]) then return 0 end
redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], 125000)
return 1
`)

// Limiter is safe for concurrent use.
type Limiter struct {
	now        func() time.Time
	liveHomes  func() int
	mu         sync.Mutex
	local      map[string]*window
	lastPruned int64
}

type window struct {
	minute    int64
	cur, prev int
}

// New returns a limiter; liveHomes may be nil (treated as 1).
func New(liveHomes func() int) *Limiter {
	return &Limiter{now: time.Now, liveHomes: liveHomes, local: map[string]*window{}}
}

// Ticket undoes a granted slot (e.g. when the candidate is then rejected).
type Ticket struct {
	l      *Limiter
	key    string
	minute int64
	redis  bool
}

// Allow consumes one slot of kind/id if under limit per minute. limit <= 0 is unlimited.
func (l *Limiter) Allow(ctx context.Context, kind, id string, limit int) (bool, Ticket) {
	if l == nil || limit <= 0 || id == "" {
		return true, Ticket{}
	}
	now := l.now()
	minute := now.Unix() / 60
	elapsedMS := now.UnixMilli() - minute*60000
	key := kind + ":" + id
	if rc := redisx.Default(); rc.Healthy() {
		rctx, cancel := rc.Ctx(ctx)
		res, err := allowScript.Run(rctx, rc.Raw(),
			[]string{rc.Key("rpm", key, strconv.FormatInt(minute, 10)), rc.Key("rpm", key, strconv.FormatInt(minute-1, 10))},
			limit, elapsedMS).Int()
		cancel()
		rc.Observe(err)
		if err == nil {
			return res == 1, Ticket{l: l, key: key, minute: minute, redis: true}
		}
	}
	homes := 1
	if l.liveHomes != nil {
		if n := l.liveHomes(); n > 1 {
			homes = n
		}
	}
	local := limit / homes
	if local < 1 {
		local = 1
	}
	return l.allowLocal(key, minute, elapsedMS, local), Ticket{l: l, key: key, minute: minute}
}

func (l *Limiter) allowLocal(key string, minute, elapsedMS int64, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if minute != l.lastPruned {
		for k, w := range l.local {
			if w.minute < minute-1 {
				delete(l.local, k)
			}
		}
		l.lastPruned = minute
	}
	w := l.local[key]
	if w == nil {
		w = &window{minute: minute}
		l.local[key] = w
	}
	switch {
	case w.minute == minute-1:
		w.prev, w.cur, w.minute = w.cur, 0, minute
	case w.minute < minute-1:
		w.prev, w.cur, w.minute = 0, 0, minute
	}
	est := float64(w.prev)*float64(60000-elapsedMS)/60000 + float64(w.cur)
	if est+1 > float64(limit) {
		return false
	}
	w.cur++
	return true
}

// Release returns a slot taken by Allow.
func (t Ticket) Release(ctx context.Context) {
	if t.l == nil {
		return
	}
	if t.redis {
		if rc := redisx.Default(); rc.Healthy() {
			rctx, cancel := rc.Ctx(ctx)
			rc.Observe(rc.Raw().Decr(rctx, rc.Key("rpm", t.key, strconv.FormatInt(t.minute, 10))).Err())
			cancel()
		}
		return
	}
	t.l.mu.Lock()
	if w := t.l.local[t.key]; w != nil && w.minute == t.minute && w.cur > 0 {
		w.cur--
	}
	t.l.mu.Unlock()
}
