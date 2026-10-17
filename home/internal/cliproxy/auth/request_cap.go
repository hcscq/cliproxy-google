package auth

import (
	"sync"
	"sync/atomic"
	"time"
)

// Per-credential request caps (hourly/daily). The scheduler only consults a
// process-local capped set (O(1), no I/O under the scheduler lock); counting and
// cap evaluation happen in the RequestCapGate implementation (Redis-backed in Home).
// Every dispatched upstream attempt counts, including retries, because that is
// what the upstream sees.

// RequestCapGate is notified of each dispatched attempt.
type RequestCapGate interface {
	OnDispatch(authID string)
}

type capGateBox struct{ g RequestCapGate }

var (
	requestCapGate   atomic.Value // RequestCapGate
	requestCapped    sync.Map     // authID -> time.Time (capped until)
	requestCapActive atomic.Int64 // number of entries, fast path when 0
)

// SetRequestCapGate installs the gate (nil disables counting).
func SetRequestCapGate(g RequestCapGate) {
	requestCapGate.Store(capGateBox{g})
}

// MarkRequestCapped excludes authID from selection until the given time; a zero
// time clears it.
func MarkRequestCapped(authID string, until time.Time) {
	if until.IsZero() || !until.After(time.Now()) {
		if _, loaded := requestCapped.LoadAndDelete(authID); loaded {
			requestCapActive.Add(-1)
		}
		return
	}
	if _, loaded := requestCapped.Swap(authID, until); !loaded {
		requestCapActive.Add(1)
	}
}

// RequestCappedUntil reports the cap release time for authID, if capped.
func RequestCappedUntil(authID string) (time.Time, bool) {
	if requestCapActive.Load() == 0 {
		return time.Time{}, false
	}
	v, ok := requestCapped.Load(authID)
	if !ok {
		return time.Time{}, false
	}
	until := v.(time.Time)
	if !until.After(time.Now()) {
		MarkRequestCapped(authID, time.Time{})
		return time.Time{}, false
	}
	return until, true
}

func requestCapAllows(authID string) bool {
	_, capped := RequestCappedUntil(authID)
	return !capped
}

func notifyRequestCapDispatch(authID string) {
	if b, _ := requestCapGate.Load().(capGateBox); b.g != nil && authID != "" {
		b.g.OnDispatch(authID)
	}
}
