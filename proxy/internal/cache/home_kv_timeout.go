package cache

import (
	"context"
	"sync/atomic"
	"time"
)

// DefaultHomeKVTimeout bounds best-effort Home KV calls on the request path.
const DefaultHomeKVTimeout = 500 * time.Millisecond

var homeKVTimeoutNanos atomic.Int64

func init() { homeKVTimeoutNanos.Store(int64(DefaultHomeKVTimeout)) }

// SetHomeKVTimeoutMillis applies routing.retry.home-kv-timeout-ms:
// 0 = default (500ms), negative = no extra bound (connection timeout only).
func SetHomeKVTimeoutMillis(ms int) {
	switch {
	case ms == 0:
		homeKVTimeoutNanos.Store(int64(DefaultHomeKVTimeout))
	case ms < 0:
		homeKVTimeoutNanos.Store(0)
	default:
		homeKVTimeoutNanos.Store(int64(time.Duration(ms) * time.Millisecond))
	}
}

// HomeKVTimeout returns the active bound (0 = none).
func HomeKVTimeout() time.Duration { return time.Duration(homeKVTimeoutNanos.Load()) }

// homeKVContext bounds one best-effort KV operation group.
func homeKVContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if d := HomeKVTimeout(); d > 0 {
		return context.WithTimeout(parent, d)
	}
	return context.WithCancel(parent)
}
