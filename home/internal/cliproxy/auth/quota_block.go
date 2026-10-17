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

var (
	quotaBlocks      sync.Map     // "authID|model" -> time.Time
	quotaBlockActive atomic.Int64 // entries, fast path when 0
)

func quotaBlockKey(authID, model string) string {
	return strings.TrimSpace(authID) + "|" + canonicalModelKey(model)
}

// MarkQuotaBlocked excludes (authID, model) from selection until the given time. It never
// shortens an existing block; a zero/past time is ignored (use ClearQuotaBlock).
func MarkQuotaBlocked(authID, model string, until time.Time) bool {
	if strings.TrimSpace(authID) == "" || canonicalModelKey(model) == "" || !until.After(time.Now()) {
		return false
	}
	key := quotaBlockKey(authID, model)
	for {
		prev, loaded := quotaBlocks.LoadOrStore(key, until)
		if !loaded {
			quotaBlockActive.Add(1)
			return true
		}
		if !prev.(time.Time).Before(until) {
			return false
		}
		if quotaBlocks.CompareAndSwap(key, prev, until) {
			return true
		}
	}
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
	if canonicalModelKey(model) != "" {
		if _, ok := quotaBlocks.LoadAndDelete(quotaBlockKey(authID, model)); ok {
			quotaBlockActive.Add(-1)
		}
		return
	}
	quotaBlocks.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), authID+"|") {
			if _, ok := quotaBlocks.LoadAndDelete(k); ok {
				quotaBlockActive.Add(-1)
			}
		}
		return true
	})
}

// QuotaBlockedUntil reports whether (authID, model) is fast-blocked.
func QuotaBlockedUntil(authID, model string) (time.Time, bool) {
	if quotaBlockActive.Load() == 0 || model == "" {
		return time.Time{}, false
	}
	key := quotaBlockKey(authID, model)
	v, ok := quotaBlocks.Load(key)
	if !ok {
		return time.Time{}, false
	}
	until := v.(time.Time)
	if !until.After(time.Now()) {
		if quotaBlocks.CompareAndDelete(key, v) {
			quotaBlockActive.Add(-1)
		}
		return time.Time{}, false
	}
	return until, true
}

func quotaBlockAllows(authID, model string) bool {
	_, blocked := QuotaBlockedUntil(authID, model)
	return !blocked
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
