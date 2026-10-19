package home

import "testing"

func TestTierMatchesRank(t *testing.T) {
	cases := []struct {
		tier, expected string
		want           bool
	}{
		{"g1-ultra-tier", "pro", true}, {"g1-pro-tier", "pro", true}, {"free-tier", "pro", false}, {"", "pro", false},
		{"g1-pro-tier", "ultra", false}, {"g1-ultra-tier", "ultra", true},
		{"free-tier", "free", true}, {"g1-pro-tier", "free", true}, {"standard-tier", "free", true},
		{"free-tier", "", true},
	}
	for _, c := range cases {
		if got := tierMatches(c.tier, c.expected); got != c.want {
			t.Errorf("tierMatches(%q,%q)=%v want %v", c.tier, c.expected, got, c.want)
		}
	}
}
