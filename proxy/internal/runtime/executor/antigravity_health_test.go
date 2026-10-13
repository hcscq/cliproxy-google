package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestAntigravityHealthCapacityEscalationAndReset(t *testing.T) {
	tr := newAntigravityHealthTracker(10)
	s := antigravityHealthSettingsFrom(nil)
	want := []time.Duration{time.Second, 10 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		if got := tr.capacity("k", s); got != w {
			t.Fatalf("step %d: got %s want %s", i, got, w)
		}
	}
	tr.success("k", time.Now())
	if got := tr.capacity("k", s); got != time.Second {
		t.Fatalf("after success got %s", got)
	}
}

func TestAntigravityHealthSlowQuarantine(t *testing.T) {
	tr := newAntigravityHealthTracker(10)
	s := antigravityHealthSettingsFrom(nil)
	now := time.Now()
	if d := tr.slow("k", now, s); d != 0 {
		t.Fatalf("first slow should not quarantine: %s", d)
	}
	if d := tr.slow("k", now.Add(time.Second), s); d != 5*time.Minute {
		t.Fatalf("second slow: %s", d)
	}
	tr.slow("k", now.Add(2*time.Second), s)
	if d := tr.slow("k", now.Add(3*time.Second), s); d != 10*time.Minute {
		t.Fatalf("escalation: %s", d)
	}
	tr.slow("k", now.Add(4*time.Second), s)
	if d := tr.slow("k", now.Add(5*time.Second), s); d != 15*time.Minute {
		t.Fatalf("cap: %s", d)
	}
}

func TestAntigravityHealthBounded(t *testing.T) {
	tr := newAntigravityHealthTracker(100)
	s := antigravityHealthSettingsFrom(nil)
	for i := 0; i < 10000; i++ {
		tr.capacity(fmt.Sprint(i), s)
	}
	if tr.len() > 100 {
		t.Fatalf("len %d", tr.len())
	}
}

func TestAntigravityTTFBErrCarriesCooldown(t *testing.T) {
	err := antigravityTTFBTimeoutErr("gemini-3.8-flash-n", 75*time.Second, 5*time.Minute)
	if err.StatusCode() != 504 || err.retryAfter == nil || *err.retryAfter != 5*time.Minute {
		t.Fatalf("bad err %+v", err)
	}
	if !strings.HasPrefix(gjson.Get(err.msg, "error.message").String(), "3.8 降级") || gjson.Get(err.msg, "error.cpa_cooldown_ms").Int() != 300000 {
		t.Fatalf("bad body %s", err.msg)
	}
}

func TestAntigravityAttemptTimerFires(t *testing.T) {
	at := newAntigravityAttemptTimer(context.Background(), 20*time.Millisecond)
	<-at.ctx.Done()
	if at.firstByte() {
		t.Fatal("expected timeout")
	}
	at2 := newAntigravityAttemptTimer(context.Background(), time.Hour)
	if !at2.firstByte() {
		t.Fatal("unexpected timeout")
	}
	at2.cancel()
}

func TestAntigravityHealthBucketPerModelFamily(t *testing.T) {
	if antigravityHealthKey("a", "gemini-3.8-flash-n") != antigravityHealthKey("a", "gemini-3.8-pro-high") {
		t.Fatal("3.8 tiers should share a bucket")
	}
	if antigravityHealthKey("a", "gemini-3.8-flash") == antigravityHealthKey("a", "gemini-2.5-flash") {
		t.Fatal("2.5 must not share 3.8 degradation")
	}
	if antigravityHealthKey("a", "gemini-3.8-flash") == antigravityHealthKey("b", "gemini-3.8-flash") {
		t.Fatal("per credential")
	}
}
