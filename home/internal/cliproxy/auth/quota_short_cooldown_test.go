package auth

import (
	"testing"
	"time"
)

func TestQuotaShortCooldownPolicy(t *testing.T) {
	defer SetQuotaShortCooldown(QuotaShortCooldown())
	SetQuotaShortCooldown(45 * time.Second)
	now := time.Date(2026, 10, 12, 5, 0, 0, 0, time.UTC)

	// No hint: fixed short cooldown, never exponential, even at a high level.
	d, _ := quotaCooldownAfterFailure(QuotaState{BackoffLevel: 11}, now, Result{Provider: "antigravity"})
	if got := d.Sub(now); got != 45*time.Second {
		t.Fatalf("no-hint cooldown = %v, want 45s", got)
	}
	// Retry hint wins (shorter or longer than the short cooldown).
	five := 5 * time.Second
	d, _ = quotaCooldownAfterFailure(QuotaState{}, now, Result{Provider: "antigravity", RetryAfter: &five})
	if d.Sub(now) != five {
		t.Fatalf("retry hint = %v", d.Sub(now))
	}
	reset := now.Add(2 * time.Hour)
	d, _ = quotaCooldownAfterFailure(QuotaState{}, now, Result{Provider: "antigravity", ResetAt: &reset})
	if !d.Equal(reset) {
		t.Fatalf("reset hint = %v", d)
	}
	// Open window is not extended by a burst of no-hint failures.
	open := QuotaState{Exceeded: true, NextRecoverAt: now.Add(20 * time.Second)}
	d, _ = quotaCooldownAfterFailure(open, now, Result{Provider: "antigravity"})
	if !d.Equal(open.NextRecoverAt) {
		t.Fatalf("open window changed: %v", d)
	}
	// Runtime update is picked up.
	SetQuotaShortCooldown(90 * time.Second)
	d, _ = quotaCooldownAfterFailure(QuotaState{}, now, Result{Provider: "antigravity"})
	if d.Sub(now) != 90*time.Second {
		t.Fatalf("hot-reloaded cooldown = %v", d.Sub(now))
	}
}
