package home

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestRequestCapTierAndWindows(t *testing.T) {
	r := &Runtime{}
	a := &coreauth.Auth{ID: "a", Metadata: map[string]any{"subscription_tier": "g1-pro-tier"}}
	if m, tier, l := r.effectiveRequestCap(a); m != "inherit" || tier != "pro" || l.Hourly != 0 || l.Daily != 0 {
		t.Fatalf("default must be unlimited: %s %s %+v", m, tier, l)
	}
	a.Metadata[RequestCapModeKey] = "custom"
	a.Metadata[RequestCapHourlyKey] = float64(5)
	a.Metadata[RequestCapDailyKey] = "50"
	if _, _, l := r.effectiveRequestCap(a); l.Hourly != 5 || l.Daily != 50 {
		t.Fatalf("custom: %+v", l)
	}
	a.Metadata[RequestCapModeKey] = "unlimited"
	if _, _, l := r.effectiveRequestCap(a); l.Hourly != 0 || l.Daily != 0 {
		t.Fatal("unlimited")
	}
	_, _, hEnd, dEnd := reqCapWindows(time.Date(2026, 10, 17, 23, 30, 0, 0, reqCapZone))
	if hEnd.In(reqCapZone).Hour() != 0 || dEnd.In(reqCapZone).Day() != 18 {
		t.Fatalf("windows %v %v", hEnd, dEnd)
	}
	g := &requestCapGate{}
	g.limits.Store(map[string]reqCapLimits{"c": {Hourly: 3}})
	g.evaluate("c", 3, 3, hEnd, dEnd)
	if _, ok := coreauth.RequestCappedUntil("c"); !ok {
		t.Fatal("should cap at limit")
	}
	g.evaluate("c", 1, 1, hEnd, dEnd)
	if _, ok := coreauth.RequestCappedUntil("c"); ok {
		t.Fatal("should release")
	}
}
