package cluster

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Retried-OK marking at ingest: a failed attempt whose request later succeeds gets
// usage.retried_ok=true, so overview stats can count final failures with a plain SUM.
// Markers live in Redis (shared by all Homes; TTL = usageRetryWindow) with a bounded
// in-process fallback. Only failures write markers; each success costs one pipelined
// EXISTS and, only when a marker is found, one indexed UPDATE.

const (
	usageRetryFailKey  = "usage-rf"
	usageRetryOKKey    = "usage-rs"
	usageRetryLocalMax = 200000
)

type usageRetryLocal struct {
	mu   sync.Mutex
	fail map[string]time.Time
	ok   map[string]time.Time
}

var usageRetryMem = &usageRetryLocal{fail: map[string]time.Time{}, ok: map[string]time.Time{}}

func (l *usageRetryLocal) put(m map[string]time.Time, id string, now time.Time) {
	if len(m) >= usageRetryLocalMax {
		for k, t := range m {
			if now.Sub(t) > usageRetryWindow {
				delete(m, k)
			}
		}
		if len(m) >= usageRetryLocalMax {
			return
		}
	}
	m[id] = now
}

func (l *usageRetryLocal) has(m map[string]time.Time, id string, now time.Time) bool {
	t, ok := m[id]
	return ok && now.Sub(t) <= usageRetryWindow
}

// usageRetryCandidate is a successful request whose earlier attempt(s) failed.
type usageRetryCandidate struct {
	requestID string
	at        time.Time
}

// usageRetryMarkBefore records failure/success markers for records about to be stored,
// sets RetriedOK on failures whose request already succeeded, and returns successes
// whose request had failed attempts (to be marked after insert).
func usageRetryMarkBefore(ctx context.Context, records []*UsageRecord) []usageRetryCandidate {
	var fails, oks []*UsageRecord
	for _, r := range records {
		if r == nil || strings.TrimSpace(r.RequestID) == "" {
			continue
		}
		if r.Failed {
			fails = append(fails, r)
		} else {
			oks = append(oks, r)
		}
	}
	if len(fails) == 0 && len(oks) == 0 {
		return nil
	}
	var out []usageRetryCandidate
	if rc := redisx.Default(); rc.Healthy() {
		rctx, cancel := rc.Ctx(ctx)
		defer cancel()
		pipe := rc.Raw().Pipeline()
		failOK := make([]interface{ Val() int64 }, len(fails))
		for i, r := range fails {
			pipe.Set(rctx, rc.Key(usageRetryFailKey, r.RequestID), 1, usageRetryWindow)
			failOK[i] = pipe.Exists(rctx, rc.Key(usageRetryOKKey, r.RequestID))
		}
		okFail := make([]interface{ Val() int64 }, len(oks))
		for i, r := range oks {
			okFail[i] = pipe.Exists(rctx, rc.Key(usageRetryFailKey, r.RequestID))
		}
		_, err := pipe.Exec(rctx)
		rc.Observe(err)
		if err == nil {
			pipe2 := rc.Raw().Pipeline()
			for i, r := range fails {
				if failOK[i].Val() > 0 {
					r.RetriedOK = true
				}
			}
			for i, r := range oks {
				if okFail[i].Val() > 0 {
					out = append(out, usageRetryCandidate{requestID: r.RequestID, at: r.Timestamp})
					pipe2.Set(rctx, rc.Key(usageRetryOKKey, r.RequestID), 1, usageRetryWindow)
				}
			}
			if len(out) > 0 {
				_, err2 := pipe2.Exec(rctx)
				rc.Observe(err2)
			}
			return out
		}
	}
	now := time.Now()
	usageRetryMem.mu.Lock()
	defer usageRetryMem.mu.Unlock()
	for _, r := range fails {
		usageRetryMem.put(usageRetryMem.fail, r.RequestID, now)
		if usageRetryMem.has(usageRetryMem.ok, r.RequestID, now) {
			r.RetriedOK = true
		}
	}
	for _, r := range oks {
		if usageRetryMem.has(usageRetryMem.fail, r.RequestID, now) {
			out = append(out, usageRetryCandidate{requestID: r.RequestID, at: r.Timestamp})
			usageRetryMem.put(usageRetryMem.ok, r.RequestID, now)
		}
	}
	return out
}

// usageRetryMarkAfter flags the stored failed attempts of candidate requests.
func usageRetryMarkAfter(ctx context.Context, db *gorm.DB, candidates []usageRetryCandidate) {
	if db == nil || len(candidates) == 0 {
		return
	}
	ids := make([]string, 0, len(candidates))
	var minAt, maxAt time.Time
	for _, c := range candidates {
		ids = append(ids, c.requestID)
		if minAt.IsZero() || c.at.Before(minAt) {
			minAt = c.at
		}
		if c.at.After(maxAt) {
			maxAt = c.at
		}
	}
	err := db.WithContext(contextOrBackground(ctx)).Table("usage").
		Where(`request_id IN ? AND failed = ? AND retried_ok = ? AND "timestamp" >= ? AND "timestamp" <= ?`, ids, true, false, minAt.Add(-usageRetryWindow), maxAt).
		Update("retried_ok", true).Error
	if err != nil {
		log.WithError(err).Warn("usage retried-ok marking ignored")
	}
}
