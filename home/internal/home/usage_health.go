package home

import (
	"context"
	"net/http"
	"strings"
	"time"

	config "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

// applyClusterHealth feeds one usage record into the cluster-wide
// (credential, model-family) health tracker and applies its cooldown verdict.
func applyClusterHealth(ctx context.Context, authID, model string, status int, body string, ttftMs, latencyMs int64, result *coreauth.Result) {
	if authID == "" || result == nil {
		return
	}
	h := coreauth.ClusterHealth()
	s := h.Settings()
	o := coreauth.HealthObservation{AuthID: authID, Model: model, TTFBMs: ttftMs}
	lower := strings.ToLower(body)
	switch {
	case status >= 200 && status < 300:
		o.Success = true
	case status == http.StatusGatewayTimeout && strings.Contains(lower, "no first byte"):
		o.Slow, o.Failed, o.TTFBMs = true, true, latencyMs
	case status == 499 && ttftMs <= 0 && latencyMs >= s.SlowClientWait.Milliseconds():
		o.Slow, o.Failed, o.TTFBMs = true, true, latencyMs
	case status == http.StatusServiceUnavailable && strings.Contains(lower, "no capacity"):
		o.Capacity, o.Failed = true, true
	case status >= 500:
		o.Failed = true
	default:
		return // quota / client errors are handled by the regular cooldowns
	}
	cd := h.Observe(ctx, o)
	if cd <= 0 || !(o.Slow || o.Capacity) {
		return
	}
	result.RetryAfter = &cd
	if status == 499 {
		// A client that gave up after a long silent wait counts against the pair.
		state := "慢"
		if cd >= time.Minute {
			state = "降级"
		}
		result.Success = false
		result.Error = &coreauth.Error{
			HTTPStatus: http.StatusGatewayTimeout,
			Message:    coreauth.HealthLabel(coreauth.HealthFamily(model)) + " " + state + ": client left before first byte",
		}
	}
}

// clusterHealthSettings overlays the panel settings (oauth.providers.
// antigravity.health.cluster) on the env/built-in defaults. Zero keeps the
// default; negative disables the absolute demotion thresholds.
func clusterHealthSettings(c config.AntigravityClusterHealthConfig) coreauth.HealthSettings {
	s := coreauth.DefaultHealthSettings()
	sec := func(v int, dst *time.Duration) {
		switch {
		case v > 0:
			*dst = time.Duration(v) * time.Second
		case v < 0:
			*dst = 0
		}
	}
	pct := func(v int, dst *float64) {
		switch {
		case v > 0:
			*dst = float64(v) / 100
		case v < 0:
			*dst = 0
		}
	}
	pos := func(v int, dst *int) {
		if v > 0 {
			*dst = v
		}
	}
	sec(c.DemoteTTFBP95Seconds, &s.DemoteTTFBP95)
	pct(c.DemoteFailPercent, &s.DemoteFail)
	if c.MaxDemotedPercent > 0 {
		s.MaxDegradedFrac = float64(c.MaxDemotedPercent) / 100
	}
	if c.ProbeFloorPercent > 0 {
		s.ProbeFloor = float64(c.ProbeFloorPercent) / 100
	}
	if c.UpstreamFailPercent > 0 {
		s.UpstreamFail = float64(c.UpstreamFailPercent) / 100
	}
	if c.UpstreamTTFBSeconds > 0 {
		s.UpstreamTTFB = time.Duration(c.UpstreamTTFBSeconds) * time.Second
	}
	pos(c.MinPeers, &s.MinPeers)
	pos(c.SlowEvents, &s.SlowEvents)
	if c.WindowSeconds > 0 {
		s.Window = time.Duration(c.WindowSeconds) * time.Second
	}
	if c.QuarantineSeconds > 0 {
		s.Quarantine = time.Duration(c.QuarantineSeconds) * time.Second
	}
	if c.QuarantineMaxSeconds > 0 {
		s.QuarantineMax = time.Duration(c.QuarantineMaxSeconds) * time.Second
	}
	if s.QuarantineMax < s.Quarantine {
		s.QuarantineMax = s.Quarantine
	}
	if c.SlowFactor > 0 {
		s.SlowFactor = c.SlowFactor
	}
	if c.SlowMinExtraSeconds > 0 {
		s.SlowMinExtra = time.Duration(c.SlowMinExtraSeconds) * time.Second
	}
	if c.FailMarginPercent > 0 {
		s.FailMargin = float64(c.FailMarginPercent) / 100
	}
	if v := c.StallCooldownSeconds; v > 0 {
		if v < 5 {
			v = 5
		}
		if v > 600 {
			v = 600
		}
		s.SubThresholdCool = time.Duration(v) * time.Second
	}
	return s
}

// applyClusterHealthSettings installs the panel health settings on this replica.
func applyClusterHealthSettings(cfg *appconfig.Config) {
	if cfg == nil {
		return
	}
	coreauth.ClusterHealth().SetSettings(clusterHealthSettings(cfg.Antigravity.Health.Cluster))
}
