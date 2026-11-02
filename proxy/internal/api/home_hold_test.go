package api

import (
	"context"
	"testing"
	"time"
)

func TestHomeWithinReadyGraceHonorsConfiguredWindows(t *testing.T) {
	prevLong := homeReadyGrace
	prevShort := homeReadyGraceUnhealthy
	homeReadyGrace = 200 * time.Millisecond
	homeReadyGraceUnhealthy = 20 * time.Millisecond
	t.Cleanup(func() {
		homeReadyGrace = prevLong
		homeReadyGraceUnhealthy = prevShort
		homeLastHealthyNS.Store(0)
	})

	homeLastHealthyNS.Store(time.Now().UnixNano())
	if !homeWithinReadyGrace() {
		t.Fatal("just-healthy should be within grace")
	}
	homeLastHealthyNS.Store(time.Now().Add(-50 * time.Millisecond).UnixNano())
	// No current client / no alternate → long grace (200ms) still covers 50ms.
	if !homeWithinReadyGrace() {
		t.Fatal("within long grace without alternate")
	}
	homeLastHealthyNS.Store(time.Now().Add(-250 * time.Millisecond).UnixNano())
	if homeWithinReadyGrace() {
		t.Fatal("past long grace must be false")
	}
}

func TestRecordHomePreDispatchUnavailableIncrements(t *testing.T) {
	before := HomePreDispatchUnavailableCount()
	recordHomePreDispatchUnavailable(context.Background(), "home_unavailable: test")
	if got := HomePreDispatchUnavailableCount(); got != before+1 {
		t.Fatalf("count = %d, want %d", got, before+1)
	}
}
