package auth

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Capacity retry: upstream "no capacity for this model right now" errors
// (503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED, capacity-flavoured 429) are not a
// problem of the credential, so instead of failing the request we run another
// complete credential round (other credentials are tried first, as usual) after
// an exponential backoff with jitter. The requested model is never changed.
// Retries only happen before anything was sent to the client: streams are only
// retried while the upstream has not returned a successful response yet.

// CapacityRetrySettings is the effective capacity retry configuration.
type CapacityRetrySettings struct {
	Enabled     bool
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultCapacityRetrySettings returns the defaults used when nothing is configured.
func DefaultCapacityRetrySettings() CapacityRetrySettings {
	return CapacityRetrySettings{Enabled: true, MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second}
}

// NewCapacityRetrySettings builds settings from raw config values; empty or
// invalid values fall back to defaults.
func NewCapacityRetrySettings(enabled *bool, maxAttempts int, baseDelay, maxDelay string) CapacityRetrySettings {
	s := DefaultCapacityRetrySettings()
	if enabled != nil {
		s.Enabled = *enabled
	}
	if maxAttempts > 0 {
		s.MaxAttempts = maxAttempts
	} else if maxAttempts < 0 {
		s.MaxAttempts = 0
	}
	if d, err := time.ParseDuration(strings.TrimSpace(baseDelay)); err == nil && d > 0 {
		s.BaseDelay = d
	}
	if d, err := time.ParseDuration(strings.TrimSpace(maxDelay)); err == nil && d > 0 {
		s.MaxDelay = d
	}
	if s.MaxDelay < s.BaseDelay {
		s.MaxDelay = s.BaseDelay
	}
	if s.MaxAttempts > 10 {
		s.MaxAttempts = 10
	}
	return s
}

// SetCapacityRetry updates capacity retry settings (hot reload safe).
func (m *Manager) SetCapacityRetry(s CapacityRetrySettings) {
	if m == nil {
		return
	}
	m.capacityRetry.Store(&s)
}

func (m *Manager) capacityRetrySettings() CapacityRetrySettings {
	if m == nil {
		return DefaultCapacityRetrySettings()
	}
	if s := m.capacityRetry.Load(); s != nil {
		return *s
	}
	return DefaultCapacityRetrySettings()
}

var capacityMarkers = []string{"model_capacity_exhausted", "no capacity available", "capacity exhausted", "server is overloaded", "overloaded_error"}

// IsCapacityError reports whether err is an upstream model-capacity failure
// (as opposed to account quota, auth or local Home availability problems).
func IsCapacityError(err error) bool {
	if err == nil {
		return false
	}
	var local *Error
	if errors.As(err, &local) && local != nil {
		switch local.Code {
		case "home_unavailable", "auth_not_found", "executor_not_found", "provider_not_found":
			return false
		}
	}
	status := statusCodeFromError(err)
	msg := strings.ToLower(err.Error())
	hasMarker := false
	for _, m := range capacityMarkers {
		if strings.Contains(msg, m) {
			hasMarker = true
			break
		}
	}
	switch status {
	case http.StatusServiceUnavailable, 529:
		if strings.Contains(msg, "quota_exhausted") {
			return false
		}
		return hasMarker || strings.Contains(msg, "unavailable") || status == 529
	case http.StatusTooManyRequests:
		return hasMarker && !strings.Contains(msg, "quota_exhausted")
	default:
		return false
	}
}

// capacityBackoff returns the delay before retry number attempt (1-based):
// exponential from BaseDelay capped at MaxDelay with ±20% jitter; an upstream
// RetryInfo hint is honoured when larger (still capped at MaxDelay).
func capacityBackoff(s CapacityRetrySettings, attempt int, hint *time.Duration, rnd func() float64) time.Duration {
	d := s.BaseDelay
	for i := 1; i < attempt && d < s.MaxDelay; i++ {
		d *= 2
	}
	if d > s.MaxDelay {
		d = s.MaxDelay
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*rnd()))
	if hint != nil && *hint > d {
		d = *hint
	}
	if d > s.MaxDelay {
		d = s.MaxDelay
	}
	return d
}

// sleepFn is replaceable in tests.
var capacitySleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// withCapacityRetry runs op and repeats it on capacity errors.
func withCapacityRetry[T any](ctx context.Context, s CapacityRetrySettings, model string, op func() (T, error)) (T, error) {
	res, err := op()
	if !s.Enabled {
		return res, err
	}
	budget := retryBudgetFrom(ctx)
	for attempt := 1; attempt <= s.MaxAttempts && err != nil && IsCapacityError(err) && !isRequestStopError(err); attempt++ {
		if ctx != nil && ctx.Err() != nil {
			return res, err
		}
		delay := capacityBackoff(s, attempt, retryAfterFromError(err), nil)
		if remaining, bounded := budget.remaining(); bounded && delay >= remaining {
			return res, err // the request-wide time budget would be exceeded
		}
		budget.resetCredentialCount()
		log.WithField("model", model).Warnf("upstream has no capacity (HTTP %d); capacity retry %d/%d in %s (same model, all credentials)", statusCodeFromError(err), attempt, s.MaxAttempts, delay.Round(10*time.Millisecond))
		if errSleep := capacitySleep(ctxOrBackground(ctx), delay); errSleep != nil {
			return res, err
		}
		res, err = op()
	}
	return res, err
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isCapacityResultError mirrors IsCapacityError for recorded results.
func isCapacityResultError(err *Error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Message)
	return strings.Contains(msg, "model_capacity_exhausted") || strings.Contains(msg, "no capacity available")
}
