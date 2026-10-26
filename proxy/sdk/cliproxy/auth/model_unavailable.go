package auth

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// unavailableModelRetryAfter is the Retry-After hint sent with the local 503.
const unavailableModelRetryAfter = 10 * time.Minute

var unavailableModelPatterns atomic.Pointer[[]string]

// SetUnavailableModels installs the model-unavailable list (see
// config.ParseUnavailableModels). nil = default list; "" or "off" = none.
func SetUnavailableModels(spec *string) {
	pats := internalconfig.ParseUnavailableModels(spec)
	unavailableModelPatterns.Store(&pats)
}

// modelUnavailable reports whether model is on the model-unavailable list.
func modelUnavailable(model string) bool {
	var pats []string
	if p := unavailableModelPatterns.Load(); p != nil {
		pats = *p
	} else {
		pats = internalconfig.ParseUnavailableModels(nil)
	}
	return internalconfig.MatchUnavailableModel(pats, model)
}

// modelUnavailableError is the immediate local 503 for a listed model: no
// credential is selected, nothing is sent upstream, nothing is cooled.
type modelUnavailableError struct {
	base *Error
}

func newModelUnavailableError(model string) error {
	return &modelUnavailableError{base: &Error{
		Code:       "model_unavailable",
		Message:    fmt.Sprintf("model %s currently unavailable upstream (no capacity)", internalconfig.NormalizeUnavailableModel(model)),
		HTTPStatus: http.StatusServiceUnavailable,
	}}
}

func (e *modelUnavailableError) Error() string { return e.base.Error() }

func (e *modelUnavailableError) StatusCode() int { return http.StatusServiceUnavailable }

func (e *modelUnavailableError) Headers() http.Header {
	return safeRetryAfterHeader(unavailableModelRetryAfter)
}

func (e *modelUnavailableError) As(target any) bool {
	if t, ok := target.(**Error); ok {
		*t = e.base
		return true
	}
	return false
}

// IsModelUnavailableError reports whether err is the local model-unavailable 503.
func IsModelUnavailableError(err error) bool {
	_, ok := err.(*modelUnavailableError)
	return ok
}
