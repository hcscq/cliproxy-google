package auth

import (
	"testing"
	"time"
)

func TestQuotaBlockExcludesPairImmediately(t *testing.T) {
	pred := schedulerPredicate("gemini-3-flash", nil, nil, "", 0, 0)
	entry := &scheduledAuth{auth: &Auth{ID: "qb1"}}
	if !pred(entry) {
		t.Fatal("unblocked pair rejected")
	}
	until := time.Now().Add(time.Hour)
	if !MarkQuotaBlocked("qb1", "gemini-3-flash", until) {
		t.Fatal("not marked")
	}
	if pred(entry) {
		t.Fatal("blocked pair still selectable")
	}
	if other := schedulerPredicate("gemini-2.5-flash", nil, nil, "", 0, 0); !other(entry) {
		t.Fatal("block leaked to another model")
	}
	if MarkQuotaBlocked("qb1", "gemini-3-flash", until.Add(-time.Minute)) {
		t.Fatal("block shortened")
	}
	ClearQuotaBlock("qb1", "gemini-3-flash")
	if !pred(entry) {
		t.Fatal("clear did not release")
	}
	MarkQuotaBlocked("qb1", "m2", time.Now().Add(20*time.Millisecond))
	time.Sleep(40 * time.Millisecond)
	if _, ok := QuotaBlockedUntil("qb1", "m2"); ok {
		t.Fatal("expired block still active")
	}
}

func TestQuotaCanaryAllowsExactlyOne(t *testing.T) {
	id, model := "canary-a", "gemini-2.5-flash"
	defer ClearQuotaBlock(id, "")
	ArmQuotaCanary(id, model, time.Now().Add(time.Hour))
	if !quotaBlockAllows(id, model) {
		t.Fatal("armed canary should allow first selection")
	}
	for i := 0; i < 5; i++ {
		if quotaBlockAllows(id, model) {
			t.Fatal("canary allowed more than one request")
		}
	}
	if !QuotaCanaryClaimed(id, model) {
		t.Fatal("canary should be claimed")
	}
	if !QuotaCanarySucceeded(id, model) || !quotaBlockAllows(id, model) {
		t.Fatal("canary success should unlock")
	}
}

func TestQuotaCanaryReblockedBy429(t *testing.T) {
	id, model := "canary-b", "gemini-2.5-flash"
	defer ClearQuotaBlock(id, "")
	reset := time.Now().Add(time.Hour)
	ArmQuotaCanary(id, model, reset)
	_ = quotaBlockAllows(id, model)
	if !MarkQuotaBlocked(id, model, reset) {
		t.Fatal("429 must convert canary into a plain block")
	}
	if quotaBlockAllows(id, model) || QuotaCanaryPending(id, model) || QuotaCanarySucceeded(id, model) {
		t.Fatal("pair must stay blocked without canary")
	}
}

func TestQuotaCanaryTimeoutReissues(t *testing.T) {
	id, model := "canary-c", "gemini-2.5-flash"
	defer ClearQuotaBlock(id, "")
	ArmQuotaCanary(id, model, time.Now().Add(time.Hour))
	_ = quotaBlockAllows(id, model)
	quotaBlockMu.RLock()
	b := quotaBlocks[quotaBlockKey(id, model)]
	quotaBlockMu.RUnlock()
	b.claimedAt.Store(time.Now().Add(-2 * quotaCanaryTimeout).UnixNano())
	if !quotaBlockAllows(id, model) || quotaBlockAllows(id, model) {
		t.Fatal("stale claim should re-issue exactly one canary")
	}
}
