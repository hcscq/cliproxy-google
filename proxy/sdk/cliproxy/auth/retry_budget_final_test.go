package auth

import (
	"context"
	"testing"
	"time"
)

func TestRetryBudgetFinalAttempt(t *testing.T) {
	if RetryBudgetFinalAttempt(context.Background(), time.Second) {
		t.Fatal("no budget must not be final")
	}
	_, ctx := newTestBudget(3, 90*time.Second)
	b := retryBudgetFrom(ctx)
	b.noteAttempt("a")
	if RetryBudgetFinalAttempt(ctx, 28*time.Second) {
		t.Fatal("first attempt with 90s budget is not final")
	}
	if !RetryBudgetFinalAttempt(ctx, 85*time.Second) {
		t.Fatal("limit leaving <10s must be final")
	}
	b.noteAttempt("b")
	b.noteAttempt("c")
	if !RetryBudgetFinalAttempt(ctx, time.Second) {
		t.Fatal("max credentials reached must be final")
	}
	_, ctx = newTestBudget(-1, -1)
	retryBudgetFrom(ctx).noteAttempt("a")
	if RetryBudgetFinalAttempt(ctx, time.Hour) {
		t.Fatal("unlimited budget is never final")
	}
}
