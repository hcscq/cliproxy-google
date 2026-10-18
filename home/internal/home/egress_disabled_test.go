package home

import "testing"

func TestEgressDisabledListExcludesFromPick(t *testing.T) {
	specs := []EgressSpec{
		{Name: "a/1.1.1.1", ProxyURL: "http://a", IP: "1.1.1.1"},
		{Name: "b/2.2.2.2", ProxyURL: "http://b", IP: "2.2.2.2", PublicIP: "9.9.9.9"},
		{Name: "c/3.3.3.3", ProxyURL: "http://c", IP: "3.3.3.3"},
	}
	settings := EgressSettingsValue{Disabled: []string{"a/1.1.1.1", " 9.9.9.9 "}}
	if !settings.IsDisabled(specs[0]) || !settings.IsDisabled(specs[1]) || settings.IsDisabled(specs[2]) {
		t.Fatal("IsDisabled mismatch")
	}
	got, ok := pickEgress(assignableEgressSpecs(specs, settings), nil, map[string]int{"http://c": 50}, true)
	if !ok || got.Name != "c/3.3.3.3" {
		t.Fatalf("picked %+v", got)
	}
	if len(assignableEgressSpecs(specs, EgressSettingsValue{})) != 3 {
		t.Fatal("empty list must not filter")
	}
}
