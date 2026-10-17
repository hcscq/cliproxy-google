package auth

import (
	"strings"
	"sync/atomic"
)

// Credential reserve (凭据储备): reserved credentials stay enabled (token refresh, quota
// fetch, tier refresh, catalog and liveness keep running and egress pinning still
// applies) but are never selected for requests. The set is held in-process, loaded by
// the Home runtime from the dedicated credential_reserve table; it never touches auth
// metadata, so no light runtime metadata persistence is involved.

var reservedCredentials atomic.Value // map[string]struct{}

// SetReservedCredentials replaces the reserved set.
func SetReservedCredentials(ids map[string]struct{}) {
	if ids == nil {
		ids = map[string]struct{}{}
	}
	reservedCredentials.Store(ids)
}

// IsCredentialReserved reports whether authID is held in reserve.
func IsCredentialReserved(authID string) bool {
	m, _ := reservedCredentials.Load().(map[string]struct{})
	if len(m) == 0 {
		return false
	}
	_, ok := m[strings.TrimSpace(authID)]
	return ok
}

// ReservedCredentialCount returns the size of the reserved set.
func ReservedCredentialCount() int {
	m, _ := reservedCredentials.Load().(map[string]struct{})
	return len(m)
}

func reserveAllows(authID string) bool { return !IsCredentialReserved(authID) }
