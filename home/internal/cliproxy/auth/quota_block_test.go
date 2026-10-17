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
