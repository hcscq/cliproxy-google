package auth

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Empty-response 502 backoff.
//
// An upstream 502 whose body is empty (or only the proxy's "empty response" /
// "Bad Gateway" text) is retried, but the generic one-minute transient cooldown
// is node-local and often never sticks, so the same (credential, model) stays
// usable and is selected again. After a configurable run of those failures the
// pair is given a short per-model next_retry_after. Other models on the
// credential are untouched. When the deadline passes the scheduler may pick the
// pair again; a success clears it, another empty 502 starts a new backoff.

const (
	defaultEmpty502Streak  = 3
	defaultEmpty502Backoff = 90 * time.Second
	minEmpty502Backoff     = 5 * time.Second
	maxEmpty502Backoff     = time.Hour
	empty502StreakCap      = 100
	empty502StreakMapLimit = 100000
)

var (
	empty502StreakThreshold atomic.Int64
	empty502BackoffNanos    atomic.Int64
	empty502Notify          atomic.Pointer[empty502NotifyHooks]
)

func init() {
	empty502StreakThreshold.Store(defaultEmpty502Streak)
	empty502BackoffNanos.Store(int64(defaultEmpty502Backoff))
}

// empty502NotifyHooks fans a newly entered or cleared backoff out to other Homes.
type empty502NotifyHooks struct {
	enter func(authID, model string, until time.Time)
	clear func(authID, model string)
}

// SetEmpty502CoolNotify installs the cross-Home fanout. Nil disables it.
func SetEmpty502CoolNotify(enter func(authID, model string, until time.Time), clear func(authID, model string)) {
	if enter == nil && clear == nil {
		empty502Notify.Store(nil)
		return
	}
	empty502Notify.Store(&empty502NotifyHooks{enter: enter, clear: clear})
}

// SetEmpty502Policy installs the panel streak threshold and backoff.
// A non-positive streak disables the feature (empty 502s then follow the
// generic transient cooldown). A non-positive backoff keeps the default.
func SetEmpty502Policy(streak int, backoff time.Duration) {
	if streak < 0 {
		streak = 0
	}
	if streak > empty502StreakCap {
		streak = empty502StreakCap
	}
	empty502StreakThreshold.Store(int64(streak))
	if backoff <= 0 {
		backoff = defaultEmpty502Backoff
	}
	if backoff < minEmpty502Backoff {
		backoff = minEmpty502Backoff
	}
	if backoff > maxEmpty502Backoff {
		backoff = maxEmpty502Backoff
	}
	empty502BackoffNanos.Store(int64(backoff))
}

// Empty502StreakThreshold is the consecutive empty-502 count that starts a backoff.
// Zero means the feature is off.
func Empty502StreakThreshold() int { return int(empty502StreakThreshold.Load()) }

// Empty502Backoff is the per-(credential, model) backoff applied at the threshold.
func Empty502Backoff() time.Duration { return time.Duration(empty502BackoffNanos.Load()) }

// Empty502CoolingEnabled reports whether consecutive empty 502s enter a backoff.
func Empty502CoolingEnabled() bool { return Empty502StreakThreshold() > 0 }

// IsEmptyResponse502 reports an empty upstream body surfaced as 502, including
// the proxy message "upstream returned an empty response (no output)" and the
// status text stored when the body itself is empty.
func IsEmptyResponse502(st *ModelState) bool {
	if st == nil {
		return false
	}
	status, msg := empty502ErrorParts(st)
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

func empty502ErrorParts(st *ModelState) (int, string) {
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

func resultIsEmptyResponse502(err *Error) bool {
	if err == nil {
		return false
	}
	return IsEmptyResponse502(&ModelState{LastError: err, StatusMessage: err.Message})
}

func empty502Key(authID, model string) string {
	return strings.TrimSpace(authID) + "\x00" + canonicalModelKey(model)
}

func (m *Manager) addEmpty502Streak(authID, model string) int {
	if m == nil {
		return 0
	}
	key := empty502Key(authID, model)
	if key == "\x00" {
		return 0
	}
	m.empty502Mu.Lock()
	defer m.empty502Mu.Unlock()
	if m.empty502 == nil {
		m.empty502 = make(map[string]int)
	}
	m.empty502[key]++
	n := m.empty502[key]
	if len(m.empty502) > empty502StreakMapLimit {
		thr := Empty502StreakThreshold()
		for k, v := range m.empty502 {
			if v < thr {
				delete(m.empty502, k)
			}
			if len(m.empty502) <= empty502StreakMapLimit/2 {
				break
			}
		}
	}
	return n
}

func (m *Manager) clearEmpty502Streak(authID, model string) {
	if m == nil {
		return
	}
	key := empty502Key(authID, model)
	m.empty502Mu.Lock()
	delete(m.empty502, key)
	m.empty502Mu.Unlock()
}

func (m *Manager) dropEmpty502Auth(authID string) {
	if m == nil {
		return
	}
	prefix := strings.TrimSpace(authID) + "\x00"
	if prefix == "\x00" {
		return
	}
	m.empty502Mu.Lock()
	for key := range m.empty502 {
		if strings.HasPrefix(key, prefix) {
			delete(m.empty502, key)
		}
	}
	m.empty502Mu.Unlock()
}

// noteEmpty502Streak updates the per-(credential, model) consecutive count.
// The returned streak is zero unless this result is an empty 502 that counts.
func (m *Manager) noteEmpty502Streak(authID, model string, result Result, disableCooling bool) int {
	if m == nil {
		return 0
	}
	if result.Success || disableCooling || !Empty502CoolingEnabled() || !resultIsEmptyResponse502(result.Error) {
		m.clearEmpty502Streak(authID, model)
		return 0
	}
	return m.addEmpty502Streak(authID, model)
}

func empty502NeedsPersist(auth *Auth, result Result, resultModel string, now time.Time, disableCooling bool, streak int) bool {
	if disableCooling || result.Success || streak <= 0 || !Empty502CoolingEnabled() {
		return false
	}
	if streak < Empty502StreakThreshold() || !resultIsEmptyResponse502(result.Error) {
		return false
	}
	state := (*ModelState)(nil)
	if auth != nil {
		state = auth.ModelStates[resultModel]
	}
	if state != nil && state.Unavailable && state.NextRetryAfter.After(now) {
		return false
	}
	return true
}

func (m *Manager) applyEmpty502Scheduling(authID, model string, transition markResultTransition, snapshot *Auth) {
	if transition.empty502Cleared {
		if ClearEmpty502Block(authID, model) {
			if h := empty502Notify.Load(); h != nil && h.clear != nil {
				h.clear(authID, model)
			}
		}
		return
	}
	if !transition.empty502Entered || snapshot == nil {
		return
	}
	st := snapshot.ModelStates[model]
	if st == nil || !st.NextRetryAfter.After(time.Now()) {
		return
	}
	MarkEmpty502Blocked(authID, model, st.NextRetryAfter)
	if h := empty502Notify.Load(); h != nil && h.enter != nil {
		h.enter(authID, model, st.NextRetryAfter)
	}
}
