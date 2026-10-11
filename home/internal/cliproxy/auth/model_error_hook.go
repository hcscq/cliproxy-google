package auth

import (
	"net/http"
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
	if status != http.StatusNotFound && status != http.StatusBadRequest {
		return
	}
	go h.fn(authID, provider, model, status, err.Message)
}
