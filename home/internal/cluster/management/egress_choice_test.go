package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestEgressPickerSpreadsBatch(t *testing.T) {
	p := &egressPicker{items: []home.EgressStatus{
		{EgressSpec: home.EgressSpec{Name: "a", ProxyURL: "http://a:1"}, Healthy: true, Credentials: 3},
		{EgressSpec: home.EgressSpec{Name: "b", ProxyURL: "http://b:1"}, Healthy: true, Credentials: 1},
		{EgressSpec: home.EgressSpec{Name: "c", ProxyURL: "http://c:1"}, Healthy: false},
	}}
	got := map[string]int{}
	for i := 0; i < 4; i++ {
		s, _ := p.pick()
		got[s.Name]++
	}
	if got["b"] != 3 || got["a"] != 1 || got["c"] != 0 {
		t.Fatalf("batch spread = %v", got)
	}
	ex := &egressPicker{explicit: &home.EgressSpec{Name: "c"}}
	if s, explicit := ex.pick(); s.Name != "c" || !explicit {
		t.Fatal("explicit choice ignored")
	}
}
