package api

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

// While the proxy switches Home (a reassignment, a Home rolling update or a
// failover) there is a short window without a healthy Home client. Requests
// arriving then wait for the new client (HOME_SWITCH_HOLD, default 10s, at most
// HOME_SWITCH_HOLD_MAX waiting requests) instead of failing with 503, and
// /readyz stays ready for HOME_READY_GRACE (default 15s) after the last healthy
// heartbeat so the Service does not drop the pod during the switch.

var (
	homeHoldWaiting   atomic.Int64
	homeLastHealthyNS atomic.Int64
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
	homeSwitchHold    = envDuration("HOME_SWITCH_HOLD", 10*time.Second)
	homeSwitchHoldMax = envInt64("HOME_SWITCH_HOLD_MAX", 20000)
	homeReadyGrace    = envDuration("HOME_READY_GRACE", 15*time.Second)
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

// homeWithinReadyGrace reports whether a healthy Home was seen recently.
func homeWithinReadyGrace() bool {
	last := homeLastHealthyNS.Load()
	return last > 0 && homeReadyGrace > 0 && time.Since(time.Unix(0, last)) < homeReadyGrace
}
