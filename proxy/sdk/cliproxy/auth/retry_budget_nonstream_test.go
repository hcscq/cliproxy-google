package auth

import (
	"context"
	"testing"
	"time"
)

// Non-stream budgets use the non-stream request deadline (240s), not
// max-retry-duration (90s); streaming budgets are unchanged.
func TestNonStreamRetryBudget(t *testing.T) {
	defer SetNonStreamRequestDeadline(0)
	m := &Manager{}
	m.SetMaxRequestCredentials(0)
	m.SetMaxRetryDuration(0) // default 90s

	// Stream: unchanged — a 120s/150s limit is final under the 90s budget.
	sctx := m.withRetryBudget(context.Background())
	retryBudgetFrom(sctx).noteAttempt("a")
	if !RetryBudgetFinalAttempt(sctx, 150*time.Second) || RetryBudgetFinalAttempt(sctx, 25*time.Second) {
		t.Fatal("stream budget behavior changed")
	}
	if rem, ok := RetryBudgetRemaining(sctx); !ok || rem > 90*time.Second {
		t.Fatalf("stream remaining=%v", rem)
	}

	// Non-stream: first 150s attempt is not final (240-150 >= 10s).
	SetNonStreamRequestDeadline(0)
	nctx := m.withNonStreamRetryBudget(context.Background())
	b := retryBudgetFrom(nctx)
	if b.maxDur != 240*time.Second || !b.nonStream {
		t.Fatalf("non-stream maxDur=%v", b.maxDur)
	}
	b.noteAttempt("a")
	if RetryBudgetFinalAttempt(nctx, 150*time.Second) {
		t.Fatal("first non-stream attempt must not be final")
	}
	// After 150s elapsed the request must still retry (not budget-exhausted).
	b.start = time.Now().Add(-151 * time.Second)
	if stop, cause := b.exhausted(ttfbTestErr{504, ttfbBody}); stop {
		t.Fatalf("non-stream retry stopped: %s", cause)
	}
	b.noteAttempt("b")
	// Second attempt: final, and remaining (~89s) bounds it.
	if !RetryBudgetFinalAttempt(nctx, 150*time.Second) {
		t.Fatal("second non-stream attempt must be final")
	}
	if rem, ok := RetryBudgetRemaining(nctx); !ok || rem > 90*time.Second || rem < 80*time.Second {
		t.Fatalf("remaining=%v", rem)
	}
	// Past the deadline the budget stops.
	b.start = time.Now().Add(-241 * time.Second)
	b.noteAttempt("c")
	if stop, _ := b.exhausted(ttfbTestErr{504, ttfbBody}); !stop {
		t.Fatal("must stop past the non-stream deadline")
	}

	// Configured / off.
	SetNonStreamRequestDeadline(300 * time.Second)
	if retryBudgetFrom(m.withNonStreamRetryBudget(context.Background())).maxDur != 300*time.Second {
		t.Fatal("configured deadline not applied")
	}
	SetNonStreamRequestDeadline(-1)
	if _, ok := RetryBudgetRemaining(m.withNonStreamRetryBudget(context.Background())); ok {
		t.Fatal("off must be unbounded")
	}
	// An existing budget is reused, not replaced.
	if m.withNonStreamRetryBudget(sctx) != sctx {
		t.Fatal("existing budget replaced")
	}
}
