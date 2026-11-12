package management

import (
	"fmt"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestPlanEgressRebalance(t *testing.T) {
	st := func(name string, healthy bool) home.EgressStatus {
		return home.EgressStatus{EgressSpec: home.EgressSpec{Name: name, ProxyURL: "http://" + name, IP: name}, Healthy: healthy}
	}
	items := []home.EgressStatus{st("a", true), st("b", true), st("c", true), st("dead", false)}
	var auths []*coreauth.Auth
	t0 := time.Now()
	for i := 0; i < 7; i++ {
		auths = append(auths, &coreauth.Auth{ID: fmt.Sprintf("x%d", i), Provider: "antigravity", ProxyURL: "http://a", CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
	}
	auths = append(auths, &coreauth.Auth{ID: "d1", Provider: "antigravity", ProxyURL: "http://dead"})
	moves := planEgressRebalance(items, auths, true)
	if len(moves) != 4 {
		t.Fatalf("moves = %+v", moves)
	}
	for _, m := range moves {
		if m.From != "a" || m.To == "dead" || m.CredentialID == "x0" {
			t.Fatalf("bad move %+v (oldest must stay, dead egress never a target)", m)
		}
	}
	if again := planEgressRebalance(items, []*coreauth.Auth{auths[0]}, true); len(again) != 0 {
		t.Fatal("balanced set should need no moves")
	}
}

func TestEgressPickerSecondaryOnly(t *testing.T) {
	items := []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "a/1", ProxyURL: "u1", Primary: true}, Healthy: true},
		{EgressSpec: home.EgressSpec{Name: "a/2", ProxyURL: "u2"}, Healthy: false},
	}
	p := &egressPicker{items: items}
	if _, ok := p.pick(); ok {
		t.Fatal("picked primary without allow_primary")
	}
	p.allowPrimary = true
	if s, ok := p.pick(); !ok || s.Name != "a/1" {
		t.Fatalf("got %v %v", s, ok)
	}
	if mv := planEgressRebalance(items, nil, false); len(mv) != 0 {
		t.Fatal("unexpected moves")
	}
}

func TestPlanEgressRebalanceSkipsBD(t *testing.T) {
	selfA := home.EgressStatus{EgressSpec: home.EgressSpec{Name: "n/a", ProxyURL: "http://a", IP: "1.1.1.1"}, Healthy: true}
	selfB := home.EgressStatus{EgressSpec: home.EgressSpec{Name: "n/b", ProxyURL: "http://b", IP: "1.1.1.2"}, Healthy: true}
	bd := home.EgressStatus{EgressSpec: home.EgressSpec{Name: "bd/198.51.100.10", Kind: home.EgressKindBD, ProxyURL: "http://u@r:3142", IP: "198.51.100.10"}, Healthy: true, Credentials: 0}
	// Many creds on A, none on BD — rebalance must not move them onto BD,
	// and must not move a BD-pinned cred off it.
	var auths []*coreauth.Auth
	t0 := time.Now()
	for i := 0; i < 4; i++ {
		auths = append(auths, &coreauth.Auth{ID: fmt.Sprintf("s%d", i), Provider: "antigravity", ProxyURL: "http://a", CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
	}
	auths = append(auths, &coreauth.Auth{ID: "bd1", Provider: "antigravity", ProxyURL: "http://u@r:3142", CreatedAt: t0})
	moves := planEgressRebalance([]home.EgressStatus{selfA, selfB, bd}, auths, true)
	for _, m := range moves {
		if m.To == "bd/198.51.100.10" || m.From == "bd/198.51.100.10" || m.CredentialID == "bd1" {
			t.Fatalf("rebalance touched BD: %+v", m)
		}
	}
}

func TestAssignableEgressStatusesDropsBD(t *testing.T) {
	items := []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "n/a", ProxyURL: "http://a"}, Healthy: true},
		{EgressSpec: home.EgressSpec{Name: "bd/1.2.3.4", Kind: home.EgressKindBD, ProxyURL: "http://bd"}, Healthy: true},
	}
	got := assignableEgressStatuses(items, home.EgressSettingsValue{})
	if len(got) != 1 || home.IsBDEgress(got[0].EgressSpec) {
		t.Fatalf("got %+v", got)
	}
}

func TestEgressPickerNeverPicksBD(t *testing.T) {
	p := &egressPicker{items: []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "bd/1.2.3.4", Kind: home.EgressKindBD, ProxyURL: "http://bd"}, Healthy: true},
		{EgressSpec: home.EgressSpec{Name: "n/a", ProxyURL: "http://a"}, Healthy: true, Credentials: 9},
	}}
	spec, ok := p.pick()
	if !ok || home.IsBDEgress(spec) || spec.Name != "n/a" {
		t.Fatalf("picked %+v %v", spec, ok)
	}
	p.items = []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "bd/1.2.3.4", Kind: home.EgressKindBD, ProxyURL: "http://bd"}, Healthy: true},
	}
	if _, ok := p.pick(); ok {
		t.Fatal("picker selected BD as the only option")
	}
}
