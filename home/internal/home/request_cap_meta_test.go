package home

import (
	"time"

	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

// The gate's inputs come from the dedicated lookup, not light metadata: a light
// auth (no tier/cap keys) resolves to "unknown", while the meta-built view
// carries the tier and custom cap.
func TestRequestCapUsesDedicatedLookupNotLightMetadata(t *testing.T) {
	r := &Runtime{}
	light := &coreauth.Auth{ID: "a1", Metadata: map[string]any{"egress_id": "e"}}
	if _, tier, _ := r.effectiveRequestCap(light); tier != "unknown" {
		t.Fatalf("light auth tier = %q", tier)
	}
	a := capAuthFromMeta("a1", RequestCapMeta{Tier: "g1-pro-tier", Mode: "custom", Hourly: "3", Daily: ""})
	mode, tier, l := r.effectiveRequestCap(a)
	if mode != "custom" || tier != "pro" || l.Hourly != 3 || l.Daily != 0 {
		t.Fatalf("got mode=%s tier=%s limits=%+v", mode, tier, l)
	}
	if _, has := a.Metadata["refresh_token"]; has {
		t.Fatal("cap view must carry no credential data")
	}
	if inherit := capAuthFromMeta("a2", RequestCapMeta{Tier: "g1-ultra-tier"}); true {
		if m, tr, _ := r.effectiveRequestCap(inherit); m != "inherit" || tr != "ultra" {
			t.Fatalf("inherit: %s %s", m, tr)
		}
	}
}

func TestRequestCapGateEvaluatesFromMetaView(t *testing.T) {
	g := &requestCapGate{r: &Runtime{}, events: make(chan string, 8)}
	g.limits.Store(map[string]reqCapLimits{})
	g.modes.Store(map[string][2]string{})
	auths := []*coreauth.Auth{capAuthFromMeta("capme", RequestCapMeta{Tier: "g1-pro-tier", Mode: "custom", Hourly: "3"})}
	g.buildAndApply(t.Context(), auths)
	if l, ok := g.limitsFor("capme"); !ok || l.Hourly != 3 {
		t.Fatalf("limits not built: %+v %v", l, ok)
	}
	_, _, hEnd, dEnd := reqCapWindows(timeNow())
	g.evaluate("capme", 3, 3, hEnd, dEnd)
	if _, capped := coreauth.RequestCappedUntil("capme"); !capped {
		t.Fatal("expected capped at 3")
	}
	coreauth.MarkRequestCapped("capme", zeroTime)
}

var zeroTime time.Time

func timeNow() time.Time { return time.Now() }
