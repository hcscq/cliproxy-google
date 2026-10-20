package auth

import "context"

type disabledProbeRefreshKey struct{}

// WithDisabledProbeRefresh marks an explicit operator/probe refresh that may
// rotate the token of a disabled credential. The credential stays disabled:
// only token fields and the refresh error are written, never status or note.
// Background auto-refresh never sets this, so disabled credentials are not
// refreshed in bulk.
func WithDisabledProbeRefresh(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disabledProbeRefreshKey{}, true)
}

// DisabledProbeRefreshAllowed reports whether ctx carries WithDisabledProbeRefresh.
func DisabledProbeRefreshAllowed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(disabledProbeRefreshKey{}).(bool)
	return v
}

// CloneErrorForRefresh exposes cloneError for cluster refresh bookkeeping.
func CloneErrorForRefresh(err *Error) *Error { return cloneError(err) }
