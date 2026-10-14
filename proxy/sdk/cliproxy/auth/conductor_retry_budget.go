package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// DefaultMaxRequestCredentials caps the distinct credentials one request
	// switches through (same model, across retry rounds) when
	// max-request-credentials is 0 (unset). Negative means unlimited.
	DefaultMaxRequestCredentials = 5
	// DefaultMaxRetryDuration bounds how long one request may keep switching
	// credentials (and waiting between retry rounds) before the last upstream
	// error is returned, so streaming clients see a first byte or an error well
	// before common 100s proxy idle limits. Used when max-retry-duration is 0;
	// negative means unlimited.
	DefaultMaxRetryDuration = 90 * time.Second
	// identicalBare429StopRun stops a request after this many consecutive
	// credentials returned the same 429 body without any quota signal, which
	// points at the request (content/identity block), not the credentials.
	identicalBare429StopRun = 4
)

type retryBudgetKey struct{}

// retryBudget is shared by every credential attempt of one client request.
type retryBudget struct {
	mu        sync.Mutex
	start     time.Time
	maxCreds  int
	maxDur    time.Duration
	attempts  int
	distinct  map[string]struct{}
	observed  int
	bareBody  string
	bareRun   int
	stopCause string
}

func normalizeMaxRequestCredentials(v int) int {
	if v == 0 {
		return DefaultMaxRequestCredentials
	}
	if v < 0 {
		return -1 // unlimited
	}
	return v
}

func normalizeMaxRetryDuration(v time.Duration) time.Duration {
	if v == 0 {
		return DefaultMaxRetryDuration
	}
	if v < 0 {
		return -1 // unlimited
	}
	return v
}

// withRetryBudget installs a request-wide budget unless one already exists.
func (m *Manager) withRetryBudget(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(retryBudgetKey{}).(*retryBudget); ok {
		return ctx
	}
	b := &retryBudget{start: time.Now()}
	if m != nil {
		b.maxCreds = normalizeMaxRequestCredentials(int(m.maxRequestCredentials.Load()))
		b.maxDur = normalizeMaxRetryDuration(time.Duration(m.maxRetryDuration.Load()))
	}
	return context.WithValue(ctx, retryBudgetKey{}, b)
}

func retryBudgetFrom(ctx context.Context) *retryBudget {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(retryBudgetKey{}).(*retryBudget)
	return b
}

// noteAttempt records that a credential is about to be tried.
func (b *retryBudget) noteAttempt(authID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.attempts++
	if b.distinct == nil {
		b.distinct = make(map[string]struct{})
	}
	b.distinct[authID] = struct{}{}
	b.mu.Unlock()
}

// remaining returns the time left in the budget (bounded=false if unlimited).
func (b *retryBudget) remaining() (time.Duration, bool) {
	if b == nil || b.maxDur <= 0 {
		return 0, false
	}
	return b.maxDur - time.Since(b.start), true
}

// wouldExceed reports whether waiting d (then retrying) overruns the budget.
func (b *retryBudget) wouldExceed(d time.Duration) bool {
	rem, bounded := b.remaining()
	return bounded && d >= rem
}

// resetCredentialCount starts a fresh credential count for a capacity retry
// round; the time budget keeps running.
func (b *retryBudget) resetCredentialCount() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.attempts, b.observed, b.bareBody, b.bareRun = 0, 0, "", 0
	b.distinct = nil
	b.mu.Unlock()
}

// exhausted reports whether the request must stop switching credentials.
// lastErr is the most recent attempt error; it is observed once per attempt.
func (b *retryBudget) exhausted(lastErr error) (bool, string) {
	if b == nil || lastErr == nil {
		return false, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopCause != "" {
		return true, b.stopCause
	}
	if b.observed < b.attempts {
		b.observed = b.attempts
		if body, ok := bareNonQuota429Body(lastErr); ok {
			if body == b.bareBody {
				b.bareRun++
			} else {
				b.bareBody, b.bareRun = body, 1
			}
		} else {
			b.bareBody, b.bareRun = "", 0
		}
	}
	switch {
	case b.bareRun >= identicalBare429StopRun:
		b.stopCause = "identical non-quota 429 across credentials"
	case b.maxCreds > 0 && len(b.distinct) >= b.maxCreds:
		b.stopCause = "max-request-credentials reached"
	case b.maxDur > 0 && time.Since(b.start) >= b.maxDur:
		b.stopCause = "max-retry-duration reached"
	}
	return b.stopCause != "", b.stopCause
}

// bareNonQuota429Body returns the normalized body of a 429 that carries no
// quota/reset signal (and is not a local cooldown short-circuit).
func bareNonQuota429Body(err error) (string, bool) {
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		return "", false
	}
	body := strings.TrimSpace(extractErrorBody(err))
	lower := strings.ToLower(body)
	for _, marker := range []string{"details", "reset", "retrydelay", "quota reached", "short cooldown", "retry after", "retry-after"} {
		if strings.Contains(lower, marker) {
			return "", false
		}
	}
	if body == "" {
		return "", false
	}
	return strings.Join(strings.Fields(body), " "), true
}

// retryBudgetStop checks the request budget before another credential is
// picked; when exhausted it returns the last upstream error wrapped as a
// request stop so no further rounds or credentials are tried.
func retryBudgetStop(ctx context.Context, lastErr, upstreamErr error) (error, bool) {
	b := retryBudgetFrom(ctx)
	stop, cause := b.exhausted(lastErr)
	if !stop {
		return nil, false
	}
	log.WithContext(ctx).Infof("credential switching stopped after %d attempt(s): %s", b.attempts, cause)
	return wrapRequestStopError(preferredExecutionAttemptError(lastErr, upstreamErr)), true
}

// minUsefulRetry is the least time worth starting another credential with.
const minUsefulRetry = 10 * time.Second

// RetryBudgetFinalAttempt reports whether the attempt now running is the last
// one the request budget allows if it were cut after limit: no credential
// switch left, or too little time after limit for another useful attempt.
// Executors use it to avoid cutting the last attempt short. False when no
// budget is installed.
func RetryBudgetFinalAttempt(ctx context.Context, limit time.Duration) bool {
	b := retryBudgetFrom(ctx)
	if b == nil {
		return false
	}
	b.mu.Lock()
	distinct := len(b.distinct)
	stopped := b.stopCause != ""
	b.mu.Unlock()
	if stopped || (b.maxCreds > 0 && distinct >= b.maxCreds) {
		return true
	}
	rem, bounded := b.remaining()
	return bounded && rem-limit < minUsefulRetry
}
