package auth

import (
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultUnavailableModels is used when the panel setting is unset: models the
// upstream lists but never serves (gemini-2.5-pro answers every credential
// with MODEL_CAPACITY_EXHAUSTED / per-model 429 since at least 2026-10-12).
const DefaultUnavailableModels = "gemini-2.5-pro"

// unavailableModelRetryAfter is the Retry-After hint sent with the local 503.
const unavailableModelRetryAfter = 10 * time.Minute

var unavailableModelPatterns atomic.Pointer[[]string]

// SetUnavailableModels installs the model-unavailable list (comma, semicolon or
// newline separated, '*' wildcards, case-insensitive). nil = default list;
// "" or "off" = none.
func SetUnavailableModels(spec *string) {
	raw := DefaultUnavailableModels
	if spec != nil {
		raw = *spec
	}
	pats := parseUnavailableModels(raw)
	unavailableModelPatterns.Store(&pats)
}

func parseUnavailableModels(raw string) []string {
	if strings.EqualFold(strings.TrimSpace(raw), "off") {
		return nil
	}
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func unavailableModelList() []string {
	if p := unavailableModelPatterns.Load(); p != nil {
		return *p
	}
	return parseUnavailableModels(DefaultUnavailableModels)
}

// normalizeUnavailableModel strips a "models/" prefix and a thinking suffix
// such as "(8192)" so "gemini-2.5-pro(8192)" matches "gemini-2.5-pro".
func normalizeUnavailableModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if i := strings.IndexByte(m, '('); i > 0 && strings.HasSuffix(m, ")") {
		m = m[:i]
	}
	return strings.TrimSpace(m)
}

// modelUnavailable reports whether model is on the model-unavailable list.
func modelUnavailable(model string) bool {
	m := normalizeUnavailableModel(model)
	if m == "" {
		return false
	}
	for _, p := range unavailableModelList() {
		if p == m {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := path.Match(p, m); ok {
				return true
			}
		}
	}
	return false
}

// modelUnavailableError is the immediate local 503 for a listed model: no
// credential is selected, nothing is sent upstream, nothing is cooled.
type modelUnavailableError struct {
	base *Error
}

func newModelUnavailableError(model string) error {
	return &modelUnavailableError{base: &Error{
		Code:       "model_unavailable",
		Message:    fmt.Sprintf("model %s currently unavailable upstream (no capacity)", normalizeUnavailableModel(model)),
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
