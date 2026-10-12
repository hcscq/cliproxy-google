package auth

import (
	"testing"
	"time"
)

// The stored BackoffLevel is the exponent for the *next* failure: a cooldown of
// base*2^n is stored with level n+1. So level 11 can carry 1024s (2^10, the 11th
// consecutive failure) or 30m (cap reached on a later failure); both are consistent.
func TestQuotaBackoffLevelSemantics(t *testing.T) {
	d, lvl := nextQuotaCooldown(10, false)
	if d != 1024*time.Second || lvl != 11 {
		t.Fatalf("level 10 -> %v level %d, want 1024s level 11", d, lvl)
	}
	d, lvl = nextQuotaCooldown(11, false)
	if d != quotaBackoffMax || lvl != 11 {
		t.Fatalf("level 11 -> %v level %d, want cap level 11", d, lvl)
	}
	d, lvl = nextQuotaCooldown(0, false)
	if d != quotaBackoffBase || lvl != 1 {
		t.Fatalf("level 0 -> %v level %d", d, lvl)
	}
}
