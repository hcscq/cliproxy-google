// Package errorbody holds the shared cap for stored upstream error bodies
// (observability.logs.error-body-max-bytes).
package errorbody

import "sync/atomic"

// DefaultMaxBytes is the default cap for stored upstream error bodies.
const DefaultMaxBytes = 64 << 10

var maxBytes atomic.Int64

// SetMaxBytes sets the cap; n <= 0 selects DefaultMaxBytes.
func SetMaxBytes(n int) {
	if n <= 0 {
		n = DefaultMaxBytes
	}
	maxBytes.Store(int64(n))
}

// MaxBytes returns the effective cap.
func MaxBytes() int {
	if n := maxBytes.Load(); n > 0 {
		return int(n)
	}
	return DefaultMaxBytes
}
