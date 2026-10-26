package auth

import (
	"net/http"
	"testing"
	"time"
)

// The attempt after (cap-1) first-byte timeouts is the last one: one more
// timeout ends the request, so it must not be cut short.
func TestRetryBudgetFinalAttemptFirstByteCap(t *testing.T) {
	defer SetMaxFirstByteTimeouts(-1)
	tt := ttfbTestErr{http.StatusGatewayTimeout, ttfbBody}
	other := ttfbTestErr{http.StatusServiceUnavailable, "no capacity"}

	cases := []struct {
		name  string
		cap   int
		errs  []error
		final bool
	}{
		{"default cap 2, no timeout yet", -1, nil, false},
		{"default cap 2, one timeout", -1, []error{tt}, true},
		{"default cap 2, other error only", -1, []error{other}, false},
		{"cap 3, one timeout", 3, []error{tt}, false},
		{"cap 3, two timeouts", 3, []error{tt, other, tt}, true},
		{"cap 1, first attempt", 1, nil, true},
		{"unlimited", 0, []error{tt, tt, tt}, false},
	}
	for _, c := range cases {
		SetMaxFirstByteTimeouts(c.cap)
		_, ctx := newTestBudget(10, 10*time.Minute)
		b := retryBudgetFrom(ctx)
		for i, e := range c.errs {
			b.noteAttempt(string(rune('a' + i)))
			if stop, _ := b.exhausted(e); stop {
				t.Fatalf("%s: unexpected stop", c.name)
			}
		}
		b.noteAttempt("next")
		if got := RetryBudgetFinalAttempt(ctx, 25*time.Second); got != c.final {
			t.Errorf("%s: final=%v want %v", c.name, got, c.final)
		}
	}
}
