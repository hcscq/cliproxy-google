package logging

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OpaqueErrorSampleSettings controls bounded capture for opaque upstream
// failures such as HTTP 500 "Unknown Error." / status UNKNOWN.
type OpaqueErrorSampleSettings struct {
	Enabled                     bool
	IncludeThinkingSignature400 bool
	PerSession                  int
	DailyCap                    int
	ExcerptBytes                int
	// Matchers are lower-case needles matched against "status|message|status_text".
	// Empty uses the built-in defaults.
	Matchers []string
}

const (
	opaqueDefaultPerSession   = 1
	opaqueDefaultDailyCap     = 50
	opaqueDefaultExcerptBytes = 8 << 10
	opaqueMaxDailyCap         = 500
	opaqueMaxExcerptBytes     = 64 << 10
	opaqueMaxPerSession       = 5
)

var (
	opaqueSettings atomic.Pointer[OpaqueErrorSampleSettings]
	opaqueLimiter  = &opaqueSampleLimiter{}
)

func init() {
	SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, IncludeThinkingSignature400: true})
}

// SetOpaqueErrorSampleSettings stores normalized opaque-error sampling config.
// Zero / empty fields fall back to conservative defaults; Enabled defaults on.
func SetOpaqueErrorSampleSettings(in OpaqueErrorSampleSettings) {
	s := in
	// Enabled defaults to true when callers pass the zero value via hot-reload
	// of an absent pointer; explicit false is preserved by passing Enabled=false
	// with Matchers non-nil sentinel handled below via a wrapper in config wiring.
	if s.PerSession <= 0 {
		s.PerSession = opaqueDefaultPerSession
	}
	if s.PerSession > opaqueMaxPerSession {
		s.PerSession = opaqueMaxPerSession
	}
	if s.DailyCap <= 0 {
		s.DailyCap = opaqueDefaultDailyCap
	}
	if s.DailyCap > opaqueMaxDailyCap {
		s.DailyCap = opaqueMaxDailyCap
	}
	if s.ExcerptBytes <= 0 {
		s.ExcerptBytes = opaqueDefaultExcerptBytes
	}
	if s.ExcerptBytes > opaqueMaxExcerptBytes {
		s.ExcerptBytes = opaqueMaxExcerptBytes
	}
	if len(s.Matchers) == 0 {
		s.Matchers = []string{"unknown error", `"status":"unknown"`, `"status": "unknown"`}
	}
	opaqueSettings.Store(&s)
}

// SetOpaqueErrorSampleEnabled sets only the enabled flag while keeping other settings.
func SetOpaqueErrorSampleEnabled(enabled bool) {
	cur := GetOpaqueErrorSampleSettings()
	cur.Enabled = enabled
	opaqueSettings.Store(&cur)
}

// GetOpaqueErrorSampleSettings returns a copy of the live settings.
func GetOpaqueErrorSampleSettings() OpaqueErrorSampleSettings {
	p := opaqueSettings.Load()
	if p == nil {
		return OpaqueErrorSampleSettings{
			Enabled: true, PerSession: opaqueDefaultPerSession, DailyCap: opaqueDefaultDailyCap,
			ExcerptBytes: opaqueDefaultExcerptBytes,
			Matchers:     []string{"unknown error", `"status":"unknown"`},
			IncludeThinkingSignature400: true,
		}
	}
	out := *p
	out.Matchers = append([]string(nil), p.Matchers...)
	return out
}

// IsOpaqueUpstreamError reports whether status+body match the configured opaque
// upstream error patterns (default: 500 Unknown Error / UNKNOWN), or optionally
// a Claude invalid-thinking-signature 400.
func IsOpaqueUpstreamError(status int, body string) bool {
	s := GetOpaqueErrorSampleSettings()
	if !s.Enabled {
		return false
	}
	lower := strings.ToLower(body)
	if status == 500 || status == 0 {
		for _, m := range s.Matchers {
			if m != "" && strings.Contains(lower, strings.ToLower(m)) {
				return true
			}
		}
	}
	if s.IncludeThinkingSignature400 && status == 400 {
		if strings.Contains(lower, "thinking") && strings.Contains(lower, "signature") && strings.Contains(lower, "invalid") {
			return true
		}
	}
	return false
}

type opaqueSampleLimiter struct {
	mu       sync.Mutex
	day      string // YYYY-MM-DD UTC
	dayCount int
	sessions map[string]int // type|session -> count today
}

func (l *opaqueSampleLimiter) admit(typ, session string, now time.Time) bool {
	s := GetOpaqueErrorSampleSettings()
	if !s.Enabled {
		return false
	}
	day := now.UTC().Format("2006-01-02")
	key := typ + "|" + session
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.day != day {
		l.day = day
		l.dayCount = 0
		l.sessions = make(map[string]int)
	}
	if l.sessions == nil {
		l.sessions = make(map[string]int)
	}
	if l.dayCount >= s.DailyCap {
		return false
	}
	if session != "" && l.sessions[key] >= s.PerSession {
		return false
	}
	// When session is empty, still count against daily cap but allow up to PerSession
	// under the empty-session bucket.
	emptyKey := typ + "|"
	if session == "" && l.sessions[emptyKey] >= s.PerSession {
		return false
	}
	if session == "" {
		key = emptyKey
	}
	l.sessions[key]++
	l.dayCount++
	return true
}

// OpaqueErrorSampleAdmit is exported for tests.
func OpaqueErrorSampleAdmit(typ, session string, now time.Time) bool {
	return opaqueLimiter.admit(typ, session, now)
}

// ResetOpaqueErrorSampleLimiterForTest clears limiter state.
func ResetOpaqueErrorSampleLimiterForTest() {
	opaqueLimiter.mu.Lock()
	defer opaqueLimiter.mu.Unlock()
	opaqueLimiter.day = ""
	opaqueLimiter.dayCount = 0
	opaqueLimiter.sessions = nil
}
