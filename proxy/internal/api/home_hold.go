package api

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// While the proxy switches Home (a reassignment, a Home rolling update or a
// failover) there is a short window without a healthy Home client. Requests
// arriving then wait for the new client (HOME_SWITCH_HOLD, default 10s, at most
// HOME_SWITCH_HOLD_MAX waiting requests) instead of failing with 503, and
// /readyz stays ready for HOME_READY_GRACE (default 15s) after the last healthy
// heartbeat so the Service does not drop the pod during a brief switch.
//
// When the heartbeat is already known unhealthy AND another live Home exists,
// /readyz uses HOME_READY_GRACE_UNHEALTHY (default 2s) so traffic drains to
// healthy proxies quickly. When there is no alternate Home (both Homes bad /
// single Home), the long grace is kept so every proxy is not removed from the
// Service at once.

var (
	homeHoldWaiting   atomic.Int64
	homeLastHealthyNS atomic.Int64
	// homePreDispatchUnavailable counts middleware/dispatch 503s that never
	// reached an executor (for ops dashboards via logs / future scrape).
	homePreDispatchUnavailable atomic.Int64
)

func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && d >= 0 {
		return d
	}
	return def
}

func envInt64(name string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64); err == nil && v >= 0 {
		return v
	}
	return def
}

var (
	homeSwitchHold          = envDuration("HOME_SWITCH_HOLD", 10*time.Second)
	homeSwitchHoldMax       = envInt64("HOME_SWITCH_HOLD_MAX", 20000)
	homeReadyGrace          = envDuration("HOME_READY_GRACE", 15*time.Second)
	homeReadyGraceUnhealthy = envDuration("HOME_READY_GRACE_UNHEALTHY", 2*time.Second)
)

// homeHealthy reports whether a healthy Home client is current and records it.
func homeHealthy() bool {
	client := home.Current()
	if client != nil && client.HeartbeatOK() {
		homeLastHealthyNS.Store(time.Now().UnixNano())
		return true
	}
	return false
}

// waitForHome blocks until a healthy Home client is current, the hold expires
// or ctx ends.
func waitForHome(ctx context.Context) bool {
	if homeHealthy() {
		return true
	}
	if homeSwitchHold <= 0 {
		return false
	}
	if homeHoldWaiting.Add(1) > homeSwitchHoldMax {
		homeHoldWaiting.Add(-1)
		return false
	}
	defer homeHoldWaiting.Add(-1)
	deadline := time.NewTimer(homeSwitchHold)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return homeHealthy()
		case <-tick.C:
			if homeHealthy() {
				return true
			}
		}
	}
}

// homeWithinReadyGrace reports whether a healthy Home was seen recently enough to keep /readyz green. Uses a short grace when an alternate Home exists so stuck proxies leave the Service; keeps the long grace when no alternate exists to avoid total Service drain.
func homeWithinReadyGrace() bool {
	last := homeLastHealthyNS.Load()
	if last <= 0 {
		return false
	}
	grace := homeReadyGrace
	client := home.Current()
	if client != nil && !client.HeartbeatOK() && client.HasAlternateHome() && homeReadyGraceUnhealthy >= 0 {
		grace = homeReadyGraceUnhealthy
	}
	return grace > 0 && time.Since(time.Unix(0, last)) < grace
}

// recordHomePreDispatchUnavailable publishes a zero-token usage row for a 503 that failed before auth selection (middleware waitForHome or equivalent).
func recordHomePreDispatchUnavailable(ctx context.Context, message string) {
	homePreDispatchUnavailable.Add(1)
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(message) == "" {
		message = "home_unavailable: control center unavailable"
	}
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:     "home",
		ExecutorType: "home-predispatch",
		Model:        "home_unavailable",
		RequestedAt:  time.Now(),
		Failed:       true,
		Fail: coreusage.Failure{
			StatusCode: 503,
			Body:       message,
		},
	})
}

// HomePreDispatchUnavailableCount returns the process-local counter.
func HomePreDispatchUnavailableCount() int64 { return homePreDispatchUnavailable.Load() }
