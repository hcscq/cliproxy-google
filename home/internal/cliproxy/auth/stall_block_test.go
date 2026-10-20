package auth

import (
	"testing"
	"time"
)

func TestMarkStallBlockedScopedAndExpires(t *testing.T) {
	defer ClearQuotaBlock("s1", "")
	if !MarkStallBlocked("s1", "gemini-3.8-flash-tiered", time.Now().Add(45*time.Second)) {
		t.Fatal("not set")
	}
	if quotaBlockAllows("s1", "gemini-3.8-flash-tiered") {
		t.Fatal("stalled pair still selectable")
	}
	if !quotaBlockAllows("s1", "gemini-2.5-flash") {
		t.Fatal("other model blocked")
	}
	// expired entries are dropped
	MarkStallBlocked("s1", "gemini-3.8-flash-low", time.Now().Add(10*time.Millisecond))
	time.Sleep(20 * time.Millisecond)
	if !quotaBlockAllows("s1", "gemini-3.8-flash-low") {
		t.Fatal("expired block still active")
	}
}

func TestMarkStallBlockedKeepsQuotaBlockAndCanary(t *testing.T) {
	defer ClearQuotaBlock("s2", "")
	reset := time.Now().Add(time.Hour)
	ArmQuotaCanary("s2", "gemini-3.8-flash-tiered", reset)
	if MarkStallBlocked("s2", "gemini-3.8-flash-tiered", time.Now().Add(45*time.Second)) {
		t.Fatal("stall must not replace a live quota block/canary")
	}
	if !QuotaCanaryPending("s2", "gemini-3.8-flash-tiered") {
		t.Fatal("canary lost")
	}
	if until, ok := QuotaBlockedUntil("s2", "gemini-3.8-flash-tiered"); !ok || !until.Equal(reset) {
		t.Fatalf("quota block changed: %v %v", until, ok)
	}
}

func TestQuotaBlockSweepBoundsMemory(t *testing.T) {
	for i := 0; i < 1000; i++ {
		MarkStallBlocked("sweep", "gemini-3.8-flash-tiered-"+string(rune('a'+i%26))+string(rune('a'+i/26)), time.Now().Add(5*time.Millisecond))
	}
	time.Sleep(10 * time.Millisecond)
	quotaBlockMu.Lock()
	quotaBlockLastSweep = time.Time{}
	sweepQuotaBlocksLocked(time.Now())
	n := 0
	for k := range quotaBlocks {
		if len(k) > 6 && k[:6] == "sweep|" {
			n++
		}
	}
	quotaBlockMu.Unlock()
	if n != 0 {
		t.Fatalf("%d expired blocks left", n)
	}
}
