// Package logretention holds the live request-log retention setting
// (observability.logs.request-log-retention-days) so the cluster maintenance
// job follows hot-reloaded config.
package logretention

import "sync/atomic"

var configured atomic.Int64

// Set stores the configured value (0 = default 30 days, negative = keep forever).
func Set(days int) { configured.Store(int64(days)) }

// Configured returns the raw configured value.
func Configured() int { return int(configured.Load()) }
