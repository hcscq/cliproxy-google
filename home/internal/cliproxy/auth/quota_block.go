package auth

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Fast quota block: an in-process (credential, model) -> until set consulted by the
// scheduler with no I/O. A Home sets it the moment a proxy reports an upstream 429
// (before the persisted transition round trip) and every other Home sets it from the
// Redis quota-sync broadcast, so no Home keeps dispatching an exhausted pair while the
// shared state catches up. It only ever excludes; the persisted cooldown state remains
// the source of truth and its explicit clears (probe success, manual reset) clear it.
//
// Canary: a successful 1-token probe before the known quota reset does not prove the
// window has room for real requests (half-exhausted windows pass tiny probes). The
// prober then keeps the pair blocked but arms a canary on exactly one Home: the first
// selection claims it and lets one real request through; its success unlocks the pair,
// a QUOTA_EXHAUSTED re-blocks it. A claimed canary with no result re-arms after
// quotaCanaryTimeout (the claim can be taken by a candidate the scheduler then drops).

const quotaCanaryTimeout = 90 * time.Second

const (
	canaryNone int32 = iota
	canaryArmed
	canaryClaimed
)

type quotaBlock struct {
	until     time.Time
	canary    atomic.Int32
	claimedAt atomic.Int64 // unix nanos
}

var (
	quotaBlockMu     sync.RWMutex
	quotaBlocks      = map[string]*quotaBlock{}
	quotaBlockActive atomic.Int64 // entries, fast path when 0
)

func quotaBlockKey(authID, model string) string {
	return strings.TrimSpace(authID) + "|" + canonicalModelKey(model)
}

func setQuotaBlockLocked(key string, b *quotaBlock) {
	if _, ok := quotaBlocks[key]; !ok {
		quotaBlockActive.Add(1)
	}
	quotaBlocks[key] = b
}

func deleteQuotaBlockLocked(key string) {
	if _, ok := quotaBlocks[key]; ok {
		delete(quotaBlocks, key)
		quotaBlockActive.Add(-1)
	}
}

// MarkQuotaBlocked excludes (authID, model) from selection until the given time. It never
// shortens an existing block; a zero/past time is ignored (use ClearQuotaBlock). A new 429
// always ends a pending canary (the pair is plainly blocked again).
func MarkQuotaBlocked(authID, model string, until time.Time) bool {
	if strings.TrimSpace(authID) == "" || canonicalModelKey(model) == "" || !until.After(time.Now()) {
		return false
	}
	key := quotaBlockKey(authID, model)
	quotaBlockMu.Lock()
	defer quotaBlockMu.Unlock()
	prev, ok := quotaBlocks[key]
	if ok && prev.canary.Load() == canaryNone && !prev.until.Before(until) {
		return false
	}
	if ok && prev.until.After(until) {
		until = prev.until
	}
	setQuotaBlockLocked(key, &quotaBlock{until: until})
	return true
}

// ArmQuotaCanary blocks (authID, model) until the given time but lets exactly one
// selection on this process through as a canary.
func ArmQuotaCanary(authID, model string, until time.Time) bool {
	if strings.TrimSpace(authID) == "" || canonicalModelKey(model) == "" || !until.After(time.Now()) {
		return false
	}
	b := &quotaBlock{until: until}
	b.canary.Store(canaryArmed)
	quotaBlockMu.Lock()
	setQuotaBlockLocked(quotaBlockKey(authID, model), b)
	quotaBlockMu.Unlock()
	return true
}

// QuotaCanaryPending reports whether (authID, model) has an armed or claimed canary here.
func QuotaCanaryPending(authID, model string) bool {
	if quotaBlockActive.Load() == 0 {
		return false
	}
	quotaBlockMu.RLock()
	b, ok := quotaBlocks[quotaBlockKey(authID, model)]
	quotaBlockMu.RUnlock()
	return ok && b.canary.Load() != canaryNone
}

// QuotaCanarySucceeded is called for a successful real result. When the pair had a claimed
// canary here, the block is removed and true is returned (caller broadcasts the unlock).
func QuotaCanarySucceeded(authID, model string) bool {
	if quotaBlockActive.Load() == 0 {
		return false
	}
	key := quotaBlockKey(authID, model)
	quotaBlockMu.Lock()
	defer quotaBlockMu.Unlock()
	b, ok := quotaBlocks[key]
	if !ok || b.canary.Load() != canaryClaimed {
		return false
	}
	deleteQuotaBlockLocked(key)
	return true
}

// QuotaCanaryClaimed reports whether (authID, model) has a claimed canary here.
func QuotaCanaryClaimed(authID, model string) bool {
	if quotaBlockActive.Load() == 0 {
		return false
	}
	quotaBlockMu.RLock()
	b, ok := quotaBlocks[quotaBlockKey(authID, model)]
	quotaBlockMu.RUnlock()
	return ok && b.canary.Load() == canaryClaimed
}

// QuotaBlockClearHook, when set, is told about explicit cooldown clears so other Homes can
// drop their fast block too (set by the Home runtime to a Redis broadcast).
var QuotaBlockClearHook atomic.Value // func(authID, model string)

func notifyQuotaBlockCleared(authID, model string) {
	if f, ok := QuotaBlockClearHook.Load().(func(string, string)); ok && f != nil {
		f(authID, model)
	}
}

// ClearQuotaBlock removes the block of (authID, model); an empty model clears all of authID.
func ClearQuotaBlock(authID, model string) {
	if quotaBlockActive.Load() == 0 {
		return
	}
	authID = strings.TrimSpace(authID)
	quotaBlockMu.Lock()
	defer quotaBlockMu.Unlock()
	if canonicalModelKey(model) != "" {
		deleteQuotaBlockLocked(quotaBlockKey(authID, model))
		return
	}
	for k := range quotaBlocks {
		if strings.HasPrefix(k, authID+"|") {
			deleteQuotaBlockLocked(k)
		}
	}
}

func lookupQuotaBlock(authID, model string) (*quotaBlock, string, bool) {
	if quotaBlockActive.Load() == 0 || model == "" {
		return nil, "", false
	}
	key := quotaBlockKey(authID, model)
	quotaBlockMu.RLock()
	b, ok := quotaBlocks[key]
	quotaBlockMu.RUnlock()
	if !ok {
		return nil, "", false
	}
	if !b.until.After(time.Now()) {
		quotaBlockMu.Lock()
		if quotaBlocks[key] == b {
			deleteQuotaBlockLocked(key)
		}
		quotaBlockMu.Unlock()
		return nil, "", false
	}
	return b, key, true
}

// QuotaBlockedUntil reports whether (authID, model) is fast-blocked (canary or not).
func QuotaBlockedUntil(authID, model string) (time.Time, bool) {
	b, _, ok := lookupQuotaBlock(authID, model)
	if !ok {
		return time.Time{}, false
	}
	return b.until, true
}

func quotaBlockAllows(authID, model string) bool {
	b, _, ok := lookupQuotaBlock(authID, model)
	if !ok {
		return true
	}
	now := time.Now()
	switch b.canary.Load() {
	case canaryArmed:
		if b.canary.CompareAndSwap(canaryArmed, canaryClaimed) {
			b.claimedAt.Store(now.UnixNano())
			return true
		}
	case canaryClaimed:
		at := b.claimedAt.Load()
		if now.Sub(time.Unix(0, at)) > quotaCanaryTimeout && b.claimedAt.CompareAndSwap(at, now.UnixNano()) {
			return true // no result in time: re-issue the canary
		}
	}
	return false
}

// QuotaBlockCount is the number of live fast blocks (diagnostics).
func QuotaBlockCount() int64 { return quotaBlockActive.Load() }

// QuotaCooldownDisabledFor reports whether 429 cooldowns are disabled for authID.
func (m *Manager) QuotaCooldownDisabledFor(authID string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	a := m.auths[strings.TrimSpace(authID)]
	m.mu.RUnlock()
	return m.quotaCooldownDisabledForAuth(a)
}

// QuotaBlockAllows reports (and, for an armed canary, claims) selection of (authID, model).
func QuotaBlockAllows(authID, model string) bool { return quotaBlockAllows(authID, model) }
