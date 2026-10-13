package home

import (
	"context"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

// applyClusterHealth feeds one usage record into the cluster-wide
// (credential, model-family) health tracker and applies its cooldown verdict.
func applyClusterHealth(ctx context.Context, authID, model string, status int, body string, ttftMs, latencyMs int64, result *coreauth.Result) {
	if authID == "" || result == nil {
		return
	}
	h := coreauth.ClusterHealth()
	s := coreauth.DefaultHealthSettings()
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
