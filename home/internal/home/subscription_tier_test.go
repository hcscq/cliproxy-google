package home

import "testing"

func TestParseSubscriptionTier(t *testing.T) {
	cases := []struct{ body, id, name string }{
		{`{"currentTier":{"id":"free-tier","name":"Antigravity"},"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"}}`, "g1-pro-tier", "Google AI Pro"},
		{`{"currentTier":{"id":"free-tier","name":"Antigravity"},"paidTier":{"id":"free-tier","name":"Antigravity Starter Quota"}}`, "free-tier", "Antigravity Starter Quota"},
		{`{"currentTier":{"id":"standard-tier","name":"Std"}}`, "standard-tier", "Std"},
	}
	for _, c := range cases {
		id, name, ok := parseSubscriptionTier([]byte(c.body))
		if !ok || id != c.id || name != c.name {
			t.Fatalf("%s: got %q %q %v", c.body, id, name, ok)
		}
	}
	if _, _, ok := parseSubscriptionTier([]byte(`{}`)); ok {
		t.Fatal("empty should not parse")
	}
}
