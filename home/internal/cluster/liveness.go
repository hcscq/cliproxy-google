package cluster

import (
	"strings"
	"time"
)

// SubscriptionLivenessWriteInterval is the minimum gap between CPA subscription
// liveness writes. The RESP ping that keeps the proxy connected stays shorter.
const SubscriptionLivenessWriteInterval = 5 * time.Second

// CPALivenessStaleWindow is how old last_seen_at may become before a membership
// is treated as dead. Samples are SubscriptionLivenessWriteInterval apart, and
// one attempt may block until lock_timeout before a short retry commits. The
// configured heartbeat timeout remains the lower bound when an operator sets
// it longer than that gap.
func CPALivenessStaleWindow(heartbeatTimeout time.Duration) time.Duration {
	if heartbeatTimeout <= 0 {
		return heartbeatTimeout
	}
	sample := SubscriptionLivenessWriteInterval + leaseLockTimeoutDuration + time.Second
	if heartbeatTimeout > sample {
		return heartbeatTimeout
	}
	return sample
}

type cpaLivenessSample struct {
	connectedAt int64
	homeIP      string
	homePort    int
	homeStarted int64
	at          time.Time
}

func (r *Repository) livenessClock() time.Time {
	if r != nil && r.livenessNow != nil {
		return r.livenessNow()
	}
	return time.Now()
}

func (r *Repository) skipCPALivenessWrite(lifetime ConnectionLifetime) bool {
	if r == nil {
		return false
	}
	fingerprint := strings.TrimSpace(lifetime.Fingerprint)
	if fingerprint == "" || lifetime.ConnectedAt.IsZero() {
		return false
	}
	now := r.livenessClock()
	r.livenessMu.Lock()
	defer r.livenessMu.Unlock()
	sample, ok := r.livenessSamples[fingerprint]
	if !ok || sample.at.IsZero() || !cpaLivenessSampleMatches(sample, lifetime) {
		return false
	}
	return now.Sub(sample.at) < SubscriptionLivenessWriteInterval
}

func (r *Repository) noteCPALivenessWrite(lifetime ConnectionLifetime) {
	if r == nil {
		return
	}
	fingerprint := strings.TrimSpace(lifetime.Fingerprint)
	if fingerprint == "" || lifetime.ConnectedAt.IsZero() {
		return
	}
	r.livenessMu.Lock()
	defer r.livenessMu.Unlock()
	if r.livenessSamples == nil {
		r.livenessSamples = make(map[string]cpaLivenessSample)
	}
	r.livenessSamples[fingerprint] = cpaLivenessSample{
		connectedAt: lifetime.ConnectedAt.UTC().UnixNano(),
		homeIP:      strings.TrimSpace(lifetime.Home.IP),
		homePort:    lifetime.Home.Port,
		homeStarted: lifetime.Home.StartedAt.UTC().UnixNano(),
		at:          r.livenessClock(),
	}
}

func cpaLivenessSampleMatches(sample cpaLivenessSample, lifetime ConnectionLifetime) bool {
	return sample.connectedAt == lifetime.ConnectedAt.UTC().UnixNano() &&
		sample.homeIP == strings.TrimSpace(lifetime.Home.IP) &&
		sample.homePort == lifetime.Home.Port &&
		sample.homeStarted == lifetime.Home.StartedAt.UTC().UnixNano()
}
