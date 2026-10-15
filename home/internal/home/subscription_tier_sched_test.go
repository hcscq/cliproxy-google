package home

import (
	"testing"
	"time"
)

func TestSubscriptionScheduleHelpers(t *testing.T) {
	for i := 0; i < 100; i++ {
		d := subscriptionJitter(time.Hour)
		if d < 54*time.Minute || d > 66*time.Minute {
			t.Fatalf("jitter out of range: %s", d)
		}
	}
	if subscriptionRetry(time.Hour) != 30*time.Minute || subscriptionRetry(5*time.Minute) != 10*time.Minute || subscriptionRetry(24*time.Hour) != time.Hour {
		t.Fatal("retry bounds")
	}
	r := &Runtime{}
	if iv, pm := r.subscriptionSettings(); iv != time.Hour || pm != 300 {
		t.Fatalf("defaults %s %d", iv, pm)
	}
	r.RequestSubscriptionCheck("a", "x")
	r.RequestSubscriptionCheck("a", "y") // debounced
	if r.subTierReq["a"] != "x" {
		t.Fatalf("debounce failed: %v", r.subTierReq)
	}
	if !subscriptionQuotaMessage(429, "Individual quota reached. Please upgrade your subscription") || subscriptionQuotaMessage(429, "rate limited") || subscriptionQuotaMessage(404, "subscription") {
		t.Fatal("quota message match")
	}
}
