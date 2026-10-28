package management

import (
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

// Per-model availability for GET /credentials/search?model=&model_status=.
// A credential supports a model when its cached Antigravity catalog lists that
// exact id (case-insensitive). "*-disabled" placeholders are not models.
// Status uses the same quota and cooldown signals as ReserveModelCapacity,
// plus an error bucket for failures that never entered cooldown — in particular
// an empty-body 502, which is retried but often not left cooling.
const (
	modelStateReady     = "ready"
	modelStateExhausted = "exhausted"
	modelStateCooling   = "cooling"
	modelStateError     = "error"
)

// classifySupportedModel reports the status of a model the catalog already
// lists. Priority: exhausted (quota window still closed), cooling (live
// per-model cooldown), error (failure with no live cooldown), ready.
func classifySupportedModel(q home.CatalogModelQuota, st *coreauth.ModelState, now time.Time) string {
	switch {
	case quotaExhausted(q, now):
		return modelStateExhausted
	case modelCooling(st, now):
		return modelStateCooling
	case uncooledModelError(st, now):
		return modelStateError
	default:
		return modelStateReady
	}
}

// quotaExhausted matches ReserveModelCapacity: remaining <= 0 and reset is
// missing or still in the future. No quotaInfo (Remaining == nil) counts as
// remaining, not exhausted. A reset already in the past is not exhausted.
func quotaExhausted(q home.CatalogModelQuota, now time.Time) bool {
	if q.Remaining == nil || *q.Remaining > 0 {
		return false
	}
	return q.Reset.IsZero() || q.Reset.After(now)
}

func modelCooling(st *coreauth.ModelState, now time.Time) bool {
	return st != nil && st.Unavailable && st.NextRetryAfter.After(now)
}

// uncooledModelError reports a model failure that is not in cooldown.
// An empty-body 502 stays in this bucket after a short cooldown expires, so
// operators can filter credentials the scheduler will keep selecting.
// Any other error is included only when no cooldown deadline was recorded
// (it was never marked cooling). Quota failures are exhausted or cooling.
func uncooledModelError(st *coreauth.ModelState, now time.Time) bool {
	if st == nil || modelCooling(st, now) {
		return false
	}
	if isEmptyResponse502(st) {
		return true
	}
	if st.Status != coreauth.StatusError || !st.NextRetryAfter.IsZero() {
		return false
	}
	if st.Quota.Exceeded {
		return false
	}
	if st.LastError != nil && st.LastError.HTTPStatus == http.StatusTooManyRequests {
		return false
	}
	return true
}

// isEmptyResponse502 reports an empty upstream body surfaced as 502, including
// the proxy message "upstream returned an empty response (no output)" and the
// status text stored when the body itself is empty.
func isEmptyResponse502(st *coreauth.ModelState) bool {
	if st == nil {
		return false
	}
	status, msg := modelErrorParts(st)
	if strings.TrimSpace(msg) == "" {
		msg = st.StatusMessage
	}
	msg = strings.TrimSpace(msg)
	lower := strings.ToLower(msg)
	empty := msg == "" || strings.EqualFold(msg, http.StatusText(http.StatusBadGateway)) ||
		strings.Contains(lower, "empty response") || strings.Contains(lower, "no output")
	if status == http.StatusBadGateway && empty {
		return true
	}
	return status == 0 && (strings.Contains(lower, "empty response") || strings.Contains(lower, "no output"))
}

func modelErrorParts(st *coreauth.ModelState) (int, string) {
	if st == nil || st.LastError == nil {
		return 0, ""
	}
	status := st.LastError.HTTPStatus
	msg := st.LastError.Message
	if st.LastError.Upstream != nil {
		if status == 0 {
			status = st.LastError.Upstream.Status
		}
		if strings.TrimSpace(msg) == "" {
			msg = string(st.LastError.Upstream.Body)
		}
	}
	return status, msg
}

// modelStateFor finds the per-model state for an id. The query is not
// rewritten; lookup is exact, then case-insensitive.
func modelStateFor(a *coreauth.Auth, model string) *coreauth.ModelState {
	if a == nil || len(a.ModelStates) == 0 || model == "" {
		return nil
	}
	if st := a.ModelStates[model]; st != nil {
		return st
	}
	low := strings.ToLower(model)
	if st := a.ModelStates[low]; st != nil {
		return st
	}
	for k, st := range a.ModelStates {
		if strings.EqualFold(k, model) {
			return st
		}
	}
	return nil
}

func (f credFilter) modelMatch(a *coreauth.Auth) bool {
	if f.model == "" {
		return true
	}
	q, ok := f.modelQuota[a.ID]
	if !ok || a == nil {
		return false
	}
	if f.modelStatus == "" {
		return true
	}
	now := f.now
	if now.IsZero() {
		now = time.Now()
	}
	return classifySupportedModel(q, modelStateFor(a, f.model), now) == f.modelStatus
}

// credentialModelFacets is the model dropdown: cached catalog support counts,
// or the static Antigravity catalog when no credential catalog is cached yet.
func credentialModelFacets() map[string]int {
	if counts := home.AntigravityModelCatalogCounts(); len(counts) > 0 {
		return counts
	}
	out := map[string]int{}
	for _, m := range registry.GetAntigravityModels() {
		if m == nil {
			continue
		}
		id := strings.TrimSpace(m.ID)
		if id == "" || strings.ContainsAny(id, "*?") || strings.HasSuffix(strings.ToLower(id), "-disabled") {
			continue
		}
		out[id] = 0
	}
	return out
}
