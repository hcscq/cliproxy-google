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
		sortCredentials(list, tc.key, tc.desc, nil, stats)
		if got := ids(list); got != tc.want {
			t.Errorf("%s desc=%v = %s, want %s", tc.key, tc.desc, got, tc.want)
		}
	}
}
