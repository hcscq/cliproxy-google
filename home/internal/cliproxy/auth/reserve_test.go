package auth

import "testing"

func TestReserveExcludesFromScheduling(t *testing.T) {
	defer SetReservedCredentials(nil)
	SetReservedCredentials(map[string]struct{}{"r1": {}})
	if reserveAllows("r1") || !reserveAllows("a1") {
		t.Fatal("reserved credential must be excluded, others allowed")
	}
	if !IsCredentialReserved(" r1 ") || ReservedCredentialCount() != 1 {
		t.Fatal("lookup")
	}
	SetReservedCredentials(nil)
	if !reserveAllows("r1") {
		t.Fatal("cleared set must allow")
	}
}
