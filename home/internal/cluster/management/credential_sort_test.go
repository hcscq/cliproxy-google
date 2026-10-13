package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func ids(list []*coreauth.Auth) string {
	s := ""
	for _, a := range list {
		s += a.ID
	}
	return s
}

func TestSortCredentials(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	mk := func(id, label string, created time.Time, egress string) *coreauth.Auth {
		a := &coreauth.Auth{ID: id, Label: label, CreatedAt: created, Metadata: map[string]any{}}
		if egress != "" {
			a.ProxyURL = "http://x"
			a.Metadata["egress_ip"] = egress
		}
		return a
	}
	list := []*coreauth.Auth{mk("a", "zed", t0, "2.2.2.2"), mk("b", "amy", t0.Add(time.Hour), ""), mk("c", "max", time.Time{}, "1.1.1.1")}
	stats := cluster.CredentialSortStats{
		LastUsed:     map[string]time.Time{"a": t0, "b": t0.Add(time.Minute)},
		MinRemaining: map[string]float64{"a": 0.9, "c": 0.1},
	}
	cases := []struct {
		key  string
		desc bool
		want string
	}{
		{sortCreated, true, "bac"}, // newest first, unknown created last
		{sortCreated, false, "abc"},
		{sortName, false, "bca"},
		{sortQuota, false, "cab"},
		{sortLastUsed, true, "bac"},
		{sortEgress, false, "cab"},
	}
	for _, tc := range cases {
		sortCredentials(list, tc.key, tc.desc, nil, stats, nil, nil)
		if got := ids(list); got != tc.want {
			t.Errorf("%s desc=%v = %s, want %s", tc.key, tc.desc, got, tc.want)
		}
	}
}

func TestSortCredentialsBySuccess(t *testing.T) {
	list := []*coreauth.Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	calls := map[string]*cluster.CredentialCallStats{"a": {Calls: 10, OK: 9}, "b": {Calls: 10, OK: 5}}
	sortCredentials(list, sortSuccess, false, nil, cluster.CredentialSortStats{}, calls, nil)
	if list[0].ID != "b" || list[1].ID != "a" || list[2].ID != "c" {
		t.Fatalf("got %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSortCredentialsBySeqAndParse(t *testing.T) {
	list := []*coreauth.Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	sortCredentials(list, sortSeq, false, nil, cluster.CredentialSortStats{}, nil, map[string]int64{"a": 3, "b": 1, "c": 2})
	if list[0].ID != "b" || list[1].ID != "c" || list[2].ID != "a" {
		t.Fatalf("got %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
	if n, only := parseSeqQuery("#12"); n != 12 || !only {
		t.Fatal("#12")
	}
	if n, only := parseSeqQuery("12"); n != 12 || only {
		t.Fatal("12")
	}
	if n, _ := parseSeqQuery("abc"); n != 0 {
		t.Fatal("abc")
	}
}
