package auth

import (
	"context"
	"testing"
	"time"
)

type budgetTestErr struct {
	code int
	body string
}

func (e budgetTestErr) Error() string        { return e.body }
func (e budgetTestErr) StatusCode() int      { return e.code }
func (e budgetTestErr) ResponseBody() []byte { return []byte(e.body) }

const bare429 = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

var budgetIDSeq int

func nextID() string {
	budgetIDSeq++
	return "auth-" + string(rune('a'+budgetIDSeq%26)) + string(rune('a'+budgetIDSeq/26%26))
}

func newTestBudget(creds int, dur time.Duration) (*Manager, context.Context) {
	m := &Manager{}
	m.SetMaxRequestCredentials(creds)
	m.SetMaxRetryDuration(dur)
	return m, m.withRetryBudget(context.Background())
}

func TestRetryBudgetDefaults(t *testing.T) {
	_, ctx := newTestBudget(0, 0)
	b := retryBudgetFrom(ctx)
	if b.maxCreds != DefaultMaxRequestCredentials || b.maxDur != DefaultMaxRetryDuration {
		t.Fatalf("defaults = %d/%s", b.maxCreds, b.maxDur)
	}
	_, ctx = newTestBudget(-1, -1)
	b = retryBudgetFrom(ctx)
	other := budgetTestErr{503, "no capacity"}
	for i := 0; i < 50; i++ {
		b.noteAttempt(nextID())
		if stop, _ := b.exhausted(other); stop {
			t.Fatal("unlimited budget stopped")
		}
	}
}

func TestRetryBudgetCredentialCap(t *testing.T) {
	_, ctx := newTestBudget(3, -1)
	b := retryBudgetFrom(ctx)
	errUp := budgetTestErr{503, "no capacity"}
	for i := 1; i <= 3; i++ {
		b.noteAttempt(nextID())
		stop, _ := b.exhausted(errUp)
		if stop != (i == 3) {
			t.Fatalf("attempt %d stop=%v", i, stop)
		}
	}
	errStop, ok := retryBudgetStop(ctx, errUp, nil)
	if !ok || !isRequestStopError(errStop) || statusCodeFromError(errStop) != 503 {
		t.Fatalf("stop err = %v ok=%v", errStop, ok)
	}
	b.resetCredentialCount()
	b.noteAttempt("same")
	b.noteAttempt("same")
	b.stopCause = ""
	if stop, _ := b.exhausted(errUp); stop {
		t.Fatal("reset count should allow another round")
	}
}

func TestRetryBudgetIdenticalBare429Run(t *testing.T) {
	_, ctx := newTestBudget(-1, -1)
	b := retryBudgetFrom(ctx)
	seq := []error{
		budgetTestErr{429, bare429},
		budgetTestErr{429, bare429},
		budgetTestErr{503, "no capacity"}, // breaks the run
		budgetTestErr{429, bare429},
		budgetTestErr{429, `{"error":{"code":429,"message":"Your quota will reset after 5s.","status":"RESOURCE_EXHAUSTED"}}`}, // quota signal breaks the run
		budgetTestErr{429, bare429},
		budgetTestErr{429, bare429},
		budgetTestErr{429, bare429},
	}
	for i, e := range seq {
		b.noteAttempt(nextID())
		stop, _ := b.exhausted(e)
		if stop {
			t.Fatalf("stopped early at %d", i)
		}
	}
	b.noteAttempt(nextID())
	if stop, cause := b.exhausted(budgetTestErr{429, bare429}); !stop || cause != "identical non-quota 429 across credentials" {
		t.Fatalf("run of %d identical bare 429s did not stop (cause %q)", identicalBare429StopRun, cause)
	}
}

func TestRetryBudgetDuration(t *testing.T) {
	_, ctx := newTestBudget(-1, 50*time.Millisecond)
	b := retryBudgetFrom(ctx)
	if !b.wouldExceed(time.Second) || b.wouldExceed(time.Millisecond) {
		t.Fatal("wouldExceed wrong")
	}
	b.noteAttempt(nextID())
	time.Sleep(60 * time.Millisecond)
	if stop, cause := b.exhausted(budgetTestErr{503, "x"}); !stop || cause != "max-retry-duration reached" {
		t.Fatalf("duration not enforced: %v %q", stop, cause)
	}
}

func TestRetryBudgetCountsDistinctCredentials(t *testing.T) {
	_, ctx := newTestBudget(2, -1)
	b := retryBudgetFrom(ctx)
	e := budgetTestErr{503, "x"}
	for i := 0; i < 5; i++ {
		b.noteAttempt("a") // retry rounds on the same credential
		if stop, _ := b.exhausted(e); stop {
			t.Fatal("same credential must not exhaust the distinct cap")
		}
	}
	b.noteAttempt("b")
	if stop, cause := b.exhausted(e); !stop || cause != "max-request-credentials reached" {
		t.Fatalf("cap not enforced: %q", cause)
	}
}
