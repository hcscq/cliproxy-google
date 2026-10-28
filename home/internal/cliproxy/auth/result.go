package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	quotaBackoffBase            = time.Second
	quotaBackoffMax             = 30 * time.Minute
	unauthorizedRetryBackoff    = time.Minute
	quotaScopeModel             = "model"
	providerQuotaHintMaxHorizon = 60 * 24 * time.Hour
)

// Result captures an upstream execution result reported by a downstream CPA node.
type Result struct {
	AuthID            string
	AuthIndex         string
	Provider          string
	Model             string
	Success           bool
	Error             *Error
	RetryAfter        *time.Duration
	ResetAt           *time.Time
	AccessTokenSHA256 string
	// RestartQuotaWindow makes a 429 start a fresh cooldown window from now even if a
	// (possibly longer) window is still open. Used by the Home quota prober.
	RestartQuotaWindow bool
}

// markResultTransition captures the registry side effects derived from a result transition.
type markResultTransition struct {
	shouldResumeModel  bool
	shouldSuspendModel bool
	suspendReason      string
	clearModelQuota    bool
	setModelQuota      bool
	empty502Entered    bool
	empty502Cleared    bool
}

// MarkResult records a downstream execution result and updates auth cooldown state.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	// Keep validation before state changes so failures leave existing data intact.
	if m == nil || (strings.TrimSpace(result.AuthID) == "" && strings.TrimSpace(result.AuthIndex) == "") {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	transition := markResultTransition{}
	transitionVersion := int64(0)
	var authSnapshot *Auth
	skipResultPersist := false
	localOnlyDisabledQuota := false
	localAvailabilityBefore := availabilityFingerprintValue{}
	disableCooling := false
	resultModel := canonicalModelKey(result.Model)
	if resultModel == "" {
		return
	}
	resultAuthID := m.resultAuthID(result)
	if resultAuthID == "" {
		return
	}
	if !result.Success {
		notifyModelError(resultAuthID, result.Provider, resultModel, result.Error)
	}
	now := time.Now()

	unlockUpdate := m.updateLocks.lock(resultAuthID)
	m.mu.Lock()
	auth := m.resultAuthLocked(result)
	if auth == nil || auth.ID != resultAuthID {
		m.mu.Unlock()
		unlockUpdate()
		return
	}
	var mutator StateMutator
	stateMutatorAvailable := false
	disableCooling = m.quotaCooldownDisabledForAuth(auth)
	auth.recordRecentRequest(now, result.Success)
	if result.Success {
		auth.Success++
	} else {
		auth.Failed++
	}
	localOnlyDisabledQuota = statusCodeFromResult(result.Error) == http.StatusTooManyRequests &&
		!isModelSupportResultError(result.Error) &&
		disableCooling &&
		disabledQuotaStateAlreadyApplied(auth, resultModel)
	if localOnlyDisabledQuota {
		localAvailabilityBefore = availabilityFingerprint(auth, resultModel)
	}
	empty502Streak := m.noteEmpty502Streak(resultAuthID, resultModel, result, disableCooling)
	if stateMutator, ok := m.store.(StateMutator); ok {
		stateMutatorAvailable = true
		if m.resultNeedsGlobalTransition(auth, result, resultModel, now, disableCooling, empty502Streak) {
			mutator = stateMutator
		}
	}
	// A locally clean token-versioned success has no state to clear. Drop its
	// transition rather than enqueue a stale row or read the database per success;
	// cluster events reconcile any newer authoritative state.
	if mutator == nil && !(stateMutatorAvailable && isTokenVersionedSuccessResult(result)) {
		transition = m.applyResultTransition(auth, result, resultModel, now, disableCooling, empty502Streak)
		if localOnlyDisabledQuota {
			skipResultPersist = availabilityFingerprint(auth, resultModel) == localAvailabilityBefore
		}
		authSnapshot = auth.Clone()
	}
	m.mu.Unlock()
	unlockUpdate()

	if mutator != nil {
		// Apply the transition against the persisted auth so concurrent quota
		// results reported to other Home nodes cannot clobber the shared state.
		persisted, errMutate := mutator.MutateAuthState(ctx, resultAuthID, func(persisted *Auth) bool {
			before := availabilityFingerprint(persisted, resultModel)
			baseVersion := persisted.StateVersion
			transition = m.applyResultTransition(persisted, result, resultModel, now, disableCooling, empty502Streak)
			changed := availabilityFingerprint(persisted, resultModel) != before
			if baseVersion > 0 {
				transitionVersion = baseVersion
				if changed {
					transitionVersion++
				}
			}
			return changed
		})
		if errMutate != nil {
			log.Warnf("auth manager: persisted result transition failed for %s: %v", resultAuthID, errMutate)
			return
		}
		if persisted == nil {
			log.Warnf("auth manager: persisted result transition returned no auth for %s", resultAuthID)
			return
		}
		if transitionVersion > 0 && persisted.StateVersion > transitionVersion {
			transition = markResultTransition{}
		}
		var adopted bool
		authSnapshot, adopted = m.adoptPersistedResultState(result, resultModel, persisted, now)
		if !adopted {
			transition = markResultTransition{}
		}
	} else if !skipResultPersist {
		persistAuthID, schedulePersist := m.stageResultPersist(ctx, authSnapshot)
		if schedulePersist {
			defer m.scheduleResultPersist(persistAuthID)
		}
	}

	m.applyEmpty502Scheduling(resultAuthID, resultModel, transition, authSnapshot)
	m.RefreshSchedulerEntry(resultAuthID)
	if authSnapshot != nil && authRefreshDisabled(authSnapshot) {
		m.queueRefreshReschedule(authSnapshot.ID)
	}
	resultStatus := statusCodeFromResult(result.Error)
	if resultStatus == http.StatusTooManyRequests || isRequestErrorCooldownStatus(resultStatus) {
		m.reconcileCoveredCooldownAfterResult(ctx, resultAuthID)
		return
	}
	if transition.clearModelQuota || transition.setModelQuota || transition.shouldResumeModel || transition.shouldSuspendModel {
		m.ReconcileRegistryModelStates(ctx, resultAuthID)
	}
}

// applyResultTransition applies the result state machine to the provided auth
// and reports the derived registry side effects. The auth may be the manager's
// in-memory copy or a persisted copy loaded by a StateMutator; the function
// uses the caller's captured cooling policy and does not read manager state.
func (m *Manager) applyResultTransition(auth *Auth, result Result, resultModel string, now time.Time, disableCooling bool, empty502Streak int) markResultTransition {
	transition := markResultTransition{}
	if auth == nil {
		return transition
	}
	if isTokenVersionFencedResult(result) && AuthIsNewerThanObserved(auth, result.AccessTokenSHA256) {
		return transition
	}
	preserveRefreshAcquisition := RefreshBlocksDispatch(auth)
	refreshStatus := auth.Status
	refreshStatusMessage := auth.StatusMessage
	refreshLastError := cloneError(auth.LastError)
	refreshNextRetryAfter := auth.NextRetryAfter
	refreshRuntimeBlocked := auth.RuntimeRefreshBlocked

	if result.Success {
		if prev := auth.ModelStates[resultModel]; prev != nil && !prev.Quota.Exceeded && IsEmptyResponse502(prev) && prev.Unavailable && !prev.NextRetryAfter.IsZero() {
			transition.empty502Cleared = true
		}
		state := ensureModelState(auth, resultModel)
		resetModelState(state, now)
		updateAggregatedAvailability(auth, now)
		if preserveRefreshAcquisition {
			auth.Status = refreshStatus
			auth.StatusMessage = refreshStatusMessage
			auth.LastError = refreshLastError
			auth.Unavailable = true
			auth.NextRetryAfter = refreshNextRetryAfter
			auth.RuntimeRefreshBlocked = refreshRuntimeBlocked
		} else if !hasModelError(auth, now) {
			auth.LastError = nil
			auth.StatusMessage = ""
			auth.Status = StatusActive
		}
		auth.UpdatedAt = now
		transition.shouldResumeModel = true
		transition.clearModelQuota = true
		return transition
	}

	if isRequestScopedNotFoundResultError(result.Error) {
		return transition
	}
	state := ensureModelState(auth, resultModel)
	state.Unavailable = true
	state.Status = StatusError
	state.UpdatedAt = now
	if result.Error != nil {
		state.LastError = cloneError(result.Error)
		state.StatusMessage = result.Error.Message
		auth.LastError = cloneError(result.Error)
		auth.StatusMessage = result.Error.Message
	}

	statusCode := statusCodeFromResult(result.Error)
	if isCapacityResultError(result.Error) {
		// Upstream had no capacity for this model right now; that says nothing
		// about the credential, so do not cool it down (the proxy retries the
		// same model with backoff).
		// The proxy escalates repeated capacity failures of one credential via
		// RetryAfter (account-level degradation); honour it when present.
		state.NextRetryAfter = time.Time{}
		state.Unavailable = false
		if !disableCooling && result.RetryAfter != nil && *result.RetryAfter > 0 {
			state.NextRetryAfter = now.Add(*result.RetryAfter)
			state.Unavailable = true
		}
	} else if isModelSupportResultError(result.Error) {
		next := now.Add(12 * time.Hour)
		state.NextRetryAfter = next
		transition.suspendReason = "model_not_supported"
		transition.shouldSuspendModel = true
	} else if empty502Streak > 0 && resultIsEmptyResponse502(result.Error) {
		// Status may live only on the upstream response (HTTPStatus 0). Counted
		// empty 502s stay selectable until the streak threshold, then this model
		// alone takes the configured short backoff.
		if Empty502CoolingEnabled() && empty502Streak >= Empty502StreakThreshold() {
			state.Unavailable = true
			if !state.NextRetryAfter.After(now) {
				state.NextRetryAfter = now.Add(Empty502Backoff())
				transition.empty502Entered = true
			}
		} else {
			state.NextRetryAfter = time.Time{}
			state.Unavailable = false
		}
	} else {
		switch statusCode {
		case http.StatusUnauthorized:
			state.NextRetryAfter = now.Add(unauthorizedRetryBackoff)
		case http.StatusPaymentRequired, http.StatusForbidden:
			if disableCooling {
				state.NextRetryAfter = time.Time{}
			} else {
				next := now.Add(30 * time.Minute)
				state.NextRetryAfter = next
				transition.suspendReason = "payment_required"
				transition.shouldSuspendModel = true
			}
		case http.StatusNotFound:
			if disableCooling {
				state.NextRetryAfter = time.Time{}
			} else {
				next := now.Add(12 * time.Hour)
				state.NextRetryAfter = next
				transition.suspendReason = "not_found"
				transition.shouldSuspendModel = true
			}
		case http.StatusTooManyRequests:
			backoffLevel := state.Quota.BackoffLevel
			if disableCooling {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
				state.QuotaResetAt = time.Time{}
				state.Quota = QuotaState{Scope: quotaScopeModel, Reason: "quota", BackoffLevel: backoffLevel}
				transition.clearModelQuota = true
				transition.shouldResumeModel = true
			} else {
				next, nextBackoffLevel := quotaCooldownAfterFailure(state.Quota, now, result)
				state.NextRetryAfter = next
				state.QuotaResetAt = time.Time{}
				state.Quota = QuotaState{
					Exceeded:      true,
					Scope:         quotaScopeModel,
					Reason:        "quota",
					NextRecoverAt: next,
					BackoffLevel:  nextBackoffLevel,
				}
				transition.suspendReason = "quota"
				transition.shouldSuspendModel = true
				transition.setModelQuota = true
			}
		case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if disableCooling {
				state.NextRetryAfter = time.Time{}
			} else if result.RetryAfter != nil && *result.RetryAfter > 0 {
				// Proxy health quarantine (slow / TTFB timeout) carries its own duration.
				state.NextRetryAfter = now.Add(*result.RetryAfter)
			} else {
				state.NextRetryAfter = now.Add(time.Minute)
			}
		default:
			state.NextRetryAfter = time.Time{}
		}
	}
	// A model-scoped failure only marks that model (state above). The credential
	// status stays as it was; credential-level errors set it elsewhere.
	auth.UpdatedAt = now
	updateAggregatedAvailability(auth, now)
	if preserveRefreshAcquisition {
		auth.Status = refreshStatus
		auth.StatusMessage = refreshStatusMessage
		auth.LastError = refreshLastError
		auth.Unavailable = true
		auth.NextRetryAfter = refreshNextRetryAfter
		auth.RuntimeRefreshBlocked = refreshRuntimeBlocked
	}
	return transition
}

// resultNeedsGlobalTransition reports whether the result must be applied to
// the persisted auth through the store's StateMutator so the transition stays
// atomic across Home nodes. Unauthorized transitions must preserve credentials
// that may be refreshed concurrently, while quota (429) and clearing successes
// need shared availability state. Other failures stay on the local path.
func (m *Manager) resultNeedsGlobalTransition(auth *Auth, result Result, resultModel string, now time.Time, disableCooling bool, empty502Streak int) bool {
	if auth == nil {
		return false
	}
	// Persist the empty-502 backoff on the shared row so every Home, and the
	// usable count, stop selecting this (credential, model). Below the threshold
	// there is nothing to write, so the request path takes no row lock.
	if empty502NeedsPersist(auth, result, resultModel, now, disableCooling, empty502Streak) {
		return true
	}
	if result.Success {
		return authHasClearableAvailabilityState(auth, resultModel, now)
	}
	statusCode := statusCodeFromResult(result.Error)
	if statusCode == http.StatusUnauthorized {
		return true
	}
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	if isModelSupportResultError(result.Error) {
		return false
	}
	if disableCooling {
		// Apply disabled 429 transitions to the persisted full auth. Minimal
		// projections intentionally omit provider credentials and must never be
		// written back as complete auth snapshots.
		return !disabledQuotaStateAlreadyApplied(auth, resultModel)
	}
	state := auth.ModelStates[resultModel]
	quota := QuotaState{}
	if state != nil {
		quota = state.Quota
	}
	next, nextBackoffLevel := quotaCooldownAfterFailure(quota, now, result)
	if !authQuotaWindowOpen(auth, resultModel, now) {
		return true
	}
	return state == nil ||
		!state.NextRetryAfter.Equal(next) ||
		!state.Quota.NextRecoverAt.Equal(next) ||
		state.Quota.BackoffLevel != nextBackoffLevel
}

// disabledQuotaStateAlreadyApplied reports whether a disabled-cooling 429 can
// update local diagnostics without changing persisted scheduling state.
func disabledQuotaStateAlreadyApplied(auth *Auth, resultModel string) bool {
	if auth == nil || resultModel == "" || auth.Status == StatusDisabled ||
		auth.Unavailable || !auth.NextRetryAfter.IsZero() ||
		fingerprintQuota(auth.Quota) != fingerprintQuota(aggregateModelQuota(auth.ModelStates)) {
		return false
	}
	state := auth.ModelStates[resultModel]
	if state == nil {
		return false
	}
	return state.Status == StatusError &&
		!state.Unavailable &&
		state.NextRetryAfter.IsZero() &&
		state.QuotaResetAt.IsZero() &&
		!state.Quota.Exceeded &&
		state.Quota.Scope == quotaScopeModel &&
		state.Quota.Reason == "quota" &&
		state.Quota.NextRecoverAt.IsZero()
}

// reconcileCoveredCooldownAfterResult derives registry state from the current
// manager view after a covered result. This closes the race where an older
// result finishes after a config reload has already disabled and cleared cooling.
func (m *Manager) reconcileCoveredCooldownAfterResult(ctx context.Context, authID string) {
	if m == nil || strings.TrimSpace(authID) == "" {
		return
	}
	auth, ok := m.GetByID(authID)
	if !ok || auth == nil {
		return
	}
	if m.quotaCooldownDisabledForAuth(auth) && hasDisabledCooldownState(auth) {
		if errClear := m.ClearDisabledCooldownStates(ctx); errClear != nil {
			log.WithError(errClear).WithField("auth_id", authID).Warn("auth manager: failed to reconcile disabled cooldown after result")
		}
	}
	// Registry state is derived even when the local auth is already clean: an
	// older transition may have reached the registry after config reconciliation.
	m.ReconcileRegistryModelStates(ctx, authID)
}

// authQuotaWindowOpen reports whether the auth (or the given model state)
// already tracks an unexpired quota cooldown window within the maximum horizon.
func authQuotaWindowOpen(auth *Auth, resultModel string, now time.Time) bool {
	if auth == nil || resultModel == "" {
		return false
	}
	state := auth.ModelStates[resultModel]
	return state != nil && state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now) && state.Quota.NextRecoverAt.Sub(now) <= providerQuotaHintMaxHorizon
}

// authHasClearableAvailabilityState reports whether a success outcome would
// clear availability state that other Home nodes can observe.
func authHasClearableAvailabilityState(auth *Auth, resultModel string, now time.Time) bool {
	if auth == nil || resultModel == "" {
		return false
	}
	if state := auth.ModelStates[resultModel]; state != nil {
		if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() || state.Status == StatusError || !state.QuotaResetAt.IsZero() {
			return true
		}
	}
	return auth.Status == StatusError && !hasModelError(auth, now)
}

// quotaFingerprint condenses the scheduling-relevant fields of a QuotaState.
type quotaFingerprint struct {
	exceeded    bool
	scope       string
	reason      string
	recoverUnix int64
	level       int
}

// availabilityFingerprintValue condenses the fields of an auth (and one model
// state) that determine scheduling availability, so state mutations can
// cheaply detect whether a transition changed anything worth persisting.
// Volatile fields such as LastError, StatusMessage, and UpdatedAt are
// intentionally excluded to keep in-window failures write-free.
type availabilityFingerprintValue struct {
	status         Status
	disabled       bool
	unavailable    bool
	nextRetryUnix  int64
	quota          quotaFingerprint
	modelPresent   bool
	modelStatus    Status
	modelUnavail   bool
	modelRetryUnix int64
	modelResetUnix int64
	modelQuota     quotaFingerprint
}

// fingerprintUnix normalizes a time for fingerprint comparison.
func fingerprintUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// fingerprintQuota builds a quota fingerprint.
func fingerprintQuota(quota QuotaState) quotaFingerprint {
	return quotaFingerprint{
		exceeded:    quota.Exceeded,
		scope:       quota.Scope,
		reason:      quota.Reason,
		recoverUnix: fingerprintUnix(quota.NextRecoverAt),
		level:       quota.BackoffLevel,
	}
}

// availabilityFingerprint builds the comparable availability view of an auth.
func availabilityFingerprint(auth *Auth, resultModel string) availabilityFingerprintValue {
	fp := availabilityFingerprintValue{}
	if auth == nil {
		return fp
	}
	fp.status = auth.Status
	fp.disabled = auth.Disabled
	fp.unavailable = auth.Unavailable
	fp.nextRetryUnix = fingerprintUnix(auth.NextRetryAfter)
	fp.quota = fingerprintQuota(auth.Quota)
	if resultModel != "" {
		if state := auth.ModelStates[resultModel]; state != nil {
			fp.modelPresent = true
			fp.modelStatus = state.Status
			fp.modelUnavail = state.Unavailable
			fp.modelRetryUnix = fingerprintUnix(state.NextRetryAfter)
			fp.modelResetUnix = fingerprintUnix(state.QuotaResetAt)
			fp.modelQuota = fingerprintQuota(state.Quota)
		}
	}
	return fp
}

// adoptPersistedCredentialLocked replaces persisted credential fields while
// retaining state that only exists in the local runtime. The caller must hold m.mu.
func (m *Manager) adoptPersistedCredentialLocked(local, persisted *Auth) {
	if m == nil || local == nil || persisted == nil {
		return
	}

	previousIndex := strings.TrimSpace(local.Index)
	localModelStates := local.ModelStates
	localStorage := local.Storage
	localRuntime := local.Runtime
	localFileName := local.FileName
	localSuccess := local.Success
	localFailed := local.Failed
	localRecentRequests := local.recentRequests
	localIndexAssigned := local.indexAssigned

	replacement := persisted.Clone()
	if strings.TrimSpace(replacement.Index) == "" {
		replacement.Index = local.Index
	}
	if strings.TrimSpace(replacement.FileName) == "" {
		replacement.FileName = localFileName
	}
	if replacement.Storage == nil {
		replacement.Storage = localStorage
	}
	if replacement.Runtime == nil {
		replacement.Runtime = localRuntime
	}
	replacement.ModelStates = localModelStates
	replacement.Success = localSuccess
	replacement.Failed = localFailed
	replacement.recentRequests = localRecentRequests
	replacement.indexAssigned = localIndexAssigned
	*local = *replacement

	nextIndex := strings.TrimSpace(local.Index)
	if previousIndex != "" && previousIndex != nextIndex {
		if indexed := m.indexAuth[previousIndex]; indexed == local {
			delete(m.indexAuth, previousIndex)
		}
	}
	if nextIndex != "" {
		if m.indexAuth == nil {
			m.indexAuth = make(map[string]*Auth)
		}
		m.indexAuth[nextIndex] = local
	}
}

// adoptPersistedResultState merges the authoritative persisted state produced
// by a StateMutator back into the manager's in-memory auth. The boolean reports
// whether that snapshot remained current enough to apply its registry effects.
func (m *Manager) adoptPersistedResultState(result Result, resultModel string, persisted *Auth, now time.Time) (*Auth, bool) {
	if persisted == nil {
		return nil, false
	}
	unlockUpdate := m.updateLocks.lock(persisted.ID)
	defer unlockUpdate()
	m.mu.Lock()
	defer m.mu.Unlock()
	local := m.auths[persisted.ID]
	if local == nil {
		return nil, false
	}
	if local.StateVersion > 0 && persisted.StateVersion > 0 && local.StateVersion > persisted.StateVersion {
		return local.Clone(), false
	}
	localRefreshBlocked := local.RuntimeRefreshBlocked ||
		(local.LastError != nil && strings.EqualFold(strings.TrimSpace(local.LastError.Code), refreshTransientErrorCode))
	adoptPersistedCredential := persisted.StateVersion > 0 && persisted.StateVersion > local.StateVersion
	if isTokenVersionFencedResult(result) {
		if AuthIsNewerThanObserved(local, result.AccessTokenSHA256) {
			return local.Clone(), false
		}
		if AuthIsNewerThanObserved(persisted, result.AccessTokenSHA256) {
			adoptPersistedCredential = true
		}
	}
	if adoptPersistedCredential {
		m.adoptPersistedCredentialLocked(local, persisted)
		local.ModelStates = mergePersistedCooldownModelStates(persisted.ModelStates, local.ModelStates)
	}
	local.Disabled = persisted.Disabled
	local.UpdatedAt = persisted.UpdatedAt
	local.StateVersion = persisted.StateVersion
	local.LastRefreshError = cloneError(persisted.LastRefreshError)
	local.NextRefreshAfter = persisted.NextRefreshAfter
	if state := persisted.ModelStates[resultModel]; state != nil {
		if local.ModelStates == nil {
			local.ModelStates = make(map[string]*ModelState)
		}
		local.ModelStates[resultModel] = state.Clone()
	} else if local.ModelStates != nil {
		delete(local.ModelStates, resultModel)
	}
	updateAggregatedAvailability(local, now)
	persistedRefreshBlocked := RefreshBlocksDispatch(persisted)
	local.RuntimeRefreshBlocked = persistedRefreshBlocked
	if localRefreshBlocked && !persistedRefreshBlocked {
		local.StatusMessage = ""
		local.LastError = nil
		var latestModelError *ModelState
		for _, state := range local.ModelStates {
			if state == nil {
				continue
			}
			activeError := state.LastError != nil ||
				(state.Status == StatusError && state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)))
			if !activeError {
				continue
			}
			if latestModelError == nil || state.UpdatedAt.After(latestModelError.UpdatedAt) {
				latestModelError = state
			}
		}
		local.Status = StatusActive
		if latestModelError != nil {
			local.StatusMessage = latestModelError.StatusMessage
			local.LastError = cloneError(latestModelError.LastError)
			if local.StatusMessage == "" && local.LastError != nil {
				local.StatusMessage = local.LastError.Message
			}
		}
	}
	// The persisted copy can report a clean active state even though this
	// node still tracks a model error that was never persisted (for example a
	// transient 5xx). Keep the local error view in that case.
	if persisted.Status != StatusActive || !hasModelError(local, now) {
		local.Status = persisted.Status
		local.StatusMessage = persisted.StatusMessage
		local.LastError = cloneError(persisted.LastError)
	}
	if persistedRefreshBlocked {
		local.Unavailable = true
		local.NextRetryAfter = persisted.NextRetryAfter
	}
	return local.Clone(), true
}

// resultAuthLocked handles a result auth locked.
func isTokenVersionedSuccessResult(result Result) bool {
	_, validHash := normalizeSHA256Hex(result.AccessTokenSHA256)
	return validHash && result.Success
}

func isTokenVersionFencedResult(result Result) bool {
	_, validHash := normalizeSHA256Hex(result.AccessTokenSHA256)
	return validHash && (result.Success || statusCodeFromResult(result.Error) == http.StatusUnauthorized)
}

func (m *Manager) resultAuthLocked(result Result) *Auth {
	if m == nil {
		return nil
	}
	if id := strings.TrimSpace(result.AuthID); id != "" {
		return m.auths[id]
	}
	if index := strings.TrimSpace(result.AuthIndex); index != "" {
		return m.indexAuth[index]
	}
	return nil
}

func (m *Manager) resultAuthID(result Result) string {
	if m == nil {
		return ""
	}
	if id := strings.TrimSpace(result.AuthID); id != "" {
		return id
	}
	index := strings.TrimSpace(result.AuthIndex)
	if index == "" {
		return ""
	}
	m.mu.RLock()
	auth := m.indexAuth[index]
	authID := ""
	if auth != nil {
		authID = strings.TrimSpace(auth.ID)
	}
	m.mu.RUnlock()
	return authID
}

// quotaCooldownDisabledForAuth reports whether cooldown scheduling is disabled
// for this auth.
func (m *Manager) quotaCooldownDisabledForAuth(auth *Auth) bool {
	if auth != nil {
		if disabled := auth.DisableCoolingOverride(); disabled != nil {
			return *disabled
		}
	}
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	return cfg != nil && cfg.DisableCooling
}

// ensureModelState ensures a model state.
func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

// resetModelState resets a model state.
func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
	state.QuotaResetAt = time.Time{}
}

// updateAggregatedAvailability updates an aggregated availability.
func updateAggregatedAvailability(auth *Auth, now time.Time) {
	// Keep validation before state changes so failures leave existing data intact.
	if auth == nil {
		return
	}
	if len(auth.ModelStates) == 0 {
		clearAggregatedAvailability(auth)
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	hasState := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		hasState = true
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if IsEmptyResponse502(state) && !state.Quota.Exceeded && state.NextRetryAfter.After(now) {
				// Empty-502 backoff is per model. It must not mark the credential unavailable.
				stateUnavailable = false
			} else if state.NextRetryAfter.IsZero() {
				stateUnavailable = false
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
	}
	if !hasState {
		clearAggregatedAvailability(auth)
		return
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	auth.Quota = aggregateModelQuota(auth.ModelStates)
}

// aggregateModelQuota builds the credential-level quota view derived from all
// model states.
func aggregateModelQuota(states map[string]*ModelState) QuotaState {
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	for _, state := range states {
		if state == nil || !state.Quota.Exceeded {
			continue
		}
		quotaExceeded = true
		if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
			quotaRecover = state.Quota.NextRecoverAt
		}
		if state.Quota.BackoffLevel > maxBackoffLevel {
			maxBackoffLevel = state.Quota.BackoffLevel
		}
	}
	if !quotaExceeded {
		return QuotaState{}
	}
	return QuotaState{
		Exceeded:      true,
		Scope:         quotaScopeModel,
		Reason:        "quota",
		NextRecoverAt: quotaRecover,
		BackoffLevel:  maxBackoffLevel,
	}
}

// clearAggregatedAvailability clears an aggregated availability.
func clearAggregatedAvailability(auth *Auth) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
}

// hasModelError reports whether model error is present.
func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

// cloneError clones an error.
func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	cloned := &Error{
		Code:       err.Code,
		Message:    err.Message,
		Diagnostic: err.Diagnostic,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
	if err.Upstream != nil {
		cloned.Upstream = &UpstreamResponse{
			Status: err.Upstream.Status,
			Body:   append([]byte(nil), err.Upstream.Body...),
		}
	}
	return cloned
}

// statusCodeFromResult derives status code from result.
func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

// isModelSupportErrorMessage reports whether model support error message.
func isModelSupportErrorMessage(message string) bool {
	// Normalize source data before building the derived payload.
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"model is not supported",
		"model not supported",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// isModelSupportResultError reports whether model support result error.
func isModelSupportResultError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return isModelSupportErrorMessage(err.Message)
}

// isRequestScopedNotFoundMessage reports whether request scoped not found message.
func isRequestScopedNotFoundMessage(message string) bool {
	if message == "" {
		return false
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

// isRequestScopedNotFoundResultError reports whether request scoped not found result error.
func isRequestScopedNotFoundResultError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusNotFound {
		return false
	}
	return isRequestScopedNotFoundMessage(err.Message)
}

// disableAuthAfterUnauthorized permanently disables a credential after a terminal refresh failure.
func disableAuthAfterUnauthorized(auth *Auth, state *ModelState, resultErr *Error, now time.Time) {
	if auth == nil {
		return
	}
	auth.Disabled = true
	auth.Unavailable = true
	auth.RuntimeRefreshBlocked = false
	auth.Status = StatusDisabled
	auth.StatusMessage = "unauthorized"
	auth.NextRetryAfter = time.Time{}
	auth.NextRefreshAfter = time.Time{}
	auth.LastRefreshError = nil
	auth.Quota = QuotaState{}
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
	} else if auth.LastError == nil {
		auth.LastError = &Error{Message: http.StatusText(http.StatusUnauthorized), HTTPStatus: http.StatusUnauthorized}
	}
	if state == nil {
		return
	}
	state.Unavailable = true
	state.Status = StatusDisabled
	state.StatusMessage = "unauthorized"
	state.NextRetryAfter = time.Time{}
	state.Quota = QuotaState{}
	state.UpdatedAt = now
	if resultErr != nil {
		state.LastError = cloneError(resultErr)
	} else if state.LastError == nil {
		state.LastError = &Error{Message: http.StatusText(http.StatusUnauthorized), HTTPStatus: http.StatusUnauthorized}
	}
}

// nextQuotaCooldown returns a next quota cooldown.
func nextQuotaCooldown(prevLevel int, disableCooling bool) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if disableCooling {
		return 0, prevLevel
	}
	if prevLevel >= 11 {
		return quotaBackoffMax, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}

// quotaShortCooldownNanos is the fixed cooldown for a 429 without an upstream hint.
// Updated from runtime config (hot-reloaded); defaults to 60s.
var quotaShortCooldownNanos atomic.Int64

func init() { quotaShortCooldownNanos.Store(int64(60 * time.Second)) }

// SetQuotaShortCooldown updates the no-hint 429 cooldown at runtime.
func SetQuotaShortCooldown(d time.Duration) {
	if d > 0 {
		quotaShortCooldownNanos.Store(int64(d))
	}
}

// QuotaShortCooldown returns the current no-hint 429 cooldown.
func QuotaShortCooldown() time.Duration { return time.Duration(quotaShortCooldownNanos.Load()) }

// quotaCooldownAfterFailure returns the recovery deadline and backoff level for a quota
// failure observed at now. Upstream hints (reset timestamp, retry delay) win. Without a
// hint the cooldown is a fixed short window (no exponential growth); the Home prober then
// decides real availability. Open windows are never shortened, so a burst of concurrent
// failures does not extend the window either.
func quotaCooldownAfterFailure(quota QuotaState, now time.Time, result Result) (time.Time, int) {
	windowOpen := !result.RestartQuotaWindow && quota.NextRecoverAt.After(now) && quota.NextRecoverAt.Sub(now) <= providerQuotaHintMaxHorizon
	// BackoffLevel still counts consecutive quota windows (diagnostics only); it no longer
	// drives the cooldown length.
	level := quota.BackoffLevel
	if !windowOpen {
		_, level = nextQuotaCooldown(quota.BackoffLevel, false)
	}
	var hinted time.Time
	if result.ResetAt != nil && result.ResetAt.After(now) && result.ResetAt.Sub(now) <= providerQuotaHintMaxHorizon {
		hinted = result.ResetAt.UTC()
	} else if result.RetryAfter != nil && *result.RetryAfter > 0 && *result.RetryAfter <= providerQuotaHintMaxHorizon {
		hinted = now.Add(*result.RetryAfter)
	}
	if !hinted.IsZero() {
		if windowOpen && quota.NextRecoverAt.After(hinted) {
			return quota.NextRecoverAt, level
		}
		return hinted, level
	}
	if windowOpen {
		return quota.NextRecoverAt, level
	}
	return now.Add(QuotaShortCooldown()), level
}

// NewUsageResult creates a new usage result.
func NewUsageResult(authIndex, provider, model string, statusCode int, body string) Result {
	// Keep validation before state changes so failures leave existing data intact.
	authIndex = strings.TrimSpace(authIndex)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	body = strings.TrimSpace(body)
	if statusCode <= 0 {
		statusCode = http.StatusOK
	}
	if statusCode == http.StatusOK {
		return Result{
			AuthIndex: authIndex,
			Provider:  provider,
			Model:     model,
			Success:   true,
		}
	}
	message := body
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		message = fmt.Sprintf("request failed with status %d", statusCode)
	}
	retryAfter, resetAt := parseUsageRetryHints(provider, body, statusCode)
	code := ""
	if statusCode == http.StatusTooManyRequests && IsModelQuotaExhaustedBody(provider, body) {
		code = ModelQuotaExhaustedCode
	}
	return Result{
		AuthIndex:  authIndex,
		Provider:   provider,
		Model:      model,
		Success:    false,
		RetryAfter: retryAfter,
		ResetAt:    resetAt,
		Error: &Error{
			Code:       code,
			Message:    message,
			HTTPStatus: statusCode,
		},
	}
}

// ModelQuotaExhaustedCode marks a per-(credential, model) cooldown caused by an explicit
// upstream QUOTA_EXHAUSTED for that model ("hidden" per-model quota: the credential's
// bucket can still show remaining quota). Such cooldowns run until the upstream reset and
// are only released early by a successful per-model probe, never by bucket ratios.
const ModelQuotaExhaustedCode = "model_quota_exhausted"

// IsModelQuotaExhaustedBody reports an Antigravity 429 whose ErrorInfo reason is QUOTA_EXHAUSTED.
func IsModelQuotaExhaustedBody(provider, body string) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "antigravity") || !strings.Contains(body, "QUOTA_EXHAUSTED") || !gjson.Valid(body) {
		return false
	}
	for _, d := range gjson.Get(body, "error.details").Array() {
		if d.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" && strings.EqualFold(strings.TrimSpace(d.Get("reason").String()), "QUOTA_EXHAUSTED") {
			return true
		}
	}
	return false
}

func parseUsageRetryHints(provider, body string, statusCode int) (*time.Duration, *time.Time) {
	// Proxy-issued health cooldown (TTFB timeout / capacity escalation).
	if statusCode >= http.StatusBadRequest && gjson.Valid(body) {
		if ms := gjson.Get(body, "error.cpa_cooldown_ms").Int(); ms > 0 {
			d := time.Duration(ms) * time.Millisecond
			return &d, nil
		}
	}
	if statusCode != http.StatusTooManyRequests || !gjson.Valid(body) {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity":
		details := gjson.Get(body, "error.details")
		if !details.Exists() || !details.IsArray() {
			return nil, nil
		}

		var retryAfter *time.Duration
		var resetAt *time.Time
		for _, detail := range details.Array() {
			switch detail.Get("@type").String() {
			case "type.googleapis.com/google.rpc.RetryInfo":
				if retryAfter == nil {
					retryAfter = parseDurationPointer(detail.Get("retryDelay").String())
				}
			case "type.googleapis.com/google.rpc.ErrorInfo":
				if resetAt == nil {
					value := strings.TrimSpace(detail.Get("metadata.quotaResetTimeStamp").String())
					if value != "" {
						parsedResetAt, errParse := time.Parse(time.RFC3339Nano, value)
						if errParse == nil {
							parsedResetAt = parsedResetAt.UTC()
							resetAt = &parsedResetAt
						}
					}
				}
			}
		}
		return retryAfter, resetAt
	case "codex":
		if strings.TrimSpace(gjson.Get(body, "error.type").String()) != "usage_limit_reached" {
			return nil, nil
		}
		var resetAt *time.Time
		if seconds, ok := parseUsageHintSeconds(gjson.Get(body, "error.resets_at")); ok {
			parsedResetAt := time.Unix(seconds, 0).UTC()
			resetAt = &parsedResetAt
		}
		var retryAfter *time.Duration
		if seconds, ok := parseUsageHintSeconds(gjson.Get(body, "error.resets_in_seconds")); ok {
			duration := time.Duration(seconds) * time.Second
			retryAfter = &duration
		}
		return retryAfter, resetAt
	default:
		return nil, nil
	}
}

func parseUsageHintSeconds(value gjson.Result) (int64, bool) {
	if value.Type != gjson.Number {
		return 0, false
	}
	seconds, errParse := strconv.ParseInt(value.Raw, 10, 64)
	const maxSeconds = int64(1<<63-1) / int64(time.Second)
	if errParse != nil || seconds <= 0 || seconds > maxSeconds {
		return 0, false
	}
	return seconds, true
}

func parseDurationPointer(value string) *time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	duration, errParse := time.ParseDuration(value)
	if errParse != nil || duration <= 0 {
		return nil
	}
	return &duration
}

// isCapacityResultError reports upstream model-capacity failures
// (MODEL_CAPACITY_EXHAUSTED / "No capacity available"), which are transient and
// not tied to the credential.
func isCapacityResultError(err *Error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Message)
	return strings.Contains(msg, "model_capacity_exhausted") || strings.Contains(msg, "no capacity available")
}

// HealthAuthID resolves the credential ID of a usage result.
func (m *Manager) HealthAuthID(result Result) string { return m.resultAuthID(result) }
