package auth

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// ModelErrorHook is notified (asynchronously) when a credential reports a
// model-level 404 or 400 for a model, e.g. to refresh that credential's
// upstream model catalog.
type ModelErrorHook func(authID, provider, model string, status int, message string)

type modelErrorHookHolder struct{ fn ModelErrorHook }

var modelErrorHooks atomic.Pointer[modelErrorHookHolder]

// SetModelErrorHook installs the process-wide model error hook.
func (m *Manager) SetModelErrorHook(hook ModelErrorHook) {
	if hook == nil {
		modelErrorHooks.Store(nil)
		return
	}
	modelErrorHooks.Store(&modelErrorHookHolder{fn: hook})
}

func notifyModelError(authID, provider, model string, err *Error) {
	h := modelErrorHooks.Load()
	if h == nil || err == nil {
		return
	}
	status := statusCodeFromResult(err)
	if status != http.StatusNotFound && status != http.StatusBadRequest && !subscriptionQuota429(status, err.Message) {
		return
	}
	go h.fn(authID, provider, model, status, err.Message)
}

// subscriptionQuota429 reports a 429 that hints at the account plan
// ("Individual quota reached. Please upgrade your subscription ...").
func subscriptionQuota429(status int, msg string) bool {
	if status != http.StatusTooManyRequests {
		return false
	}
	l := strings.ToLower(msg)
	return strings.Contains(l, "subscription") || strings.Contains(l, "individual quota")
}
