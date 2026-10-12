package auth

import (
	"context"
	"net/http"
	"time"
)

// LegacyModelScopedErrorStatus reports whether auth carries a credential status=error
// that was only left behind by model-scoped failures (older Homes set it on every
// per-model failure). Credential-level errors keep Unavailable, a refresh block or a
// 401/403 last error and are not matched.
func LegacyModelScopedErrorStatus(auth *Auth, now time.Time) bool {
	if auth == nil || auth.Disabled || auth.Status != StatusError {
		return false
	}
	if auth.RuntimeRefreshBlocked || RefreshBlocksDispatch(auth) {
		return false
	}
	if auth.Quota.Exceeded && auth.Quota.Scope != quotaScopeModel {
		return false
	}
	if auth.Unavailable || auth.NextRetryAfter.After(now) {
		// Only acceptable when it is the aggregate of model cooldowns: every model
		// state is cooling (or disabled).
		if len(auth.ModelStates) == 0 {
			return false
		}
		for _, st := range auth.ModelStates {
			if st == nil || st.Status == StatusDisabled {
				continue
			}
			if !st.Unavailable {
				return false
			}
		}
	}
	for _, st := range auth.ModelStates {
		if st != nil && st.LastError != nil {
			switch st.LastError.HTTPStatus {
			case http.StatusUnauthorized, http.StatusForbidden:
				return false
			}
		}
	}
	if auth.LastError != nil {
		switch statusCodeFromResult(auth.LastError) {
		case http.StatusUnauthorized, http.StatusForbidden:
			return false
		}
		if isRefreshAcquisitionError(auth.LastError) {
			return false
		}
	}
	return true
}

// NormalizeModelScopedErrorStatuses flips legacy model-scoped status=error credentials
// back to active (model states are untouched) and persists them. Returns the count.
func (m *Manager) NormalizeModelScopedErrorStatuses(ctx context.Context) int {
	if m == nil {
		return 0
	}
	now := time.Now()
	n := 0
	for _, a := range m.List() {
		if !LegacyModelScopedErrorStatus(a, now) {
			continue
		}
		a.Status = StatusActive
		if _, err := m.Update(ctx, a); err == nil {
			n++
		}
	}
	return n
}
