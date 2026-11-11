package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Focused error-context capture: an opt-in, bounded, in-memory, self-expiring
// buffer that records the FULL systemInstruction (plus light metadata) of up to
// N failed Antigravity requests of a chosen error type. It exists only to
// locate the deep trigger text inside client system prompts that still receive
// an instant false 429 after prefix rewrites.
//
// It stores the full system prompt, which is sensitive, so it is DEFAULT OFF
// and must be turned on deliberately via env. It never writes disk or DB, caps
// both the number of samples and each sample's size, stops after N captures,
// and auto-stops and clears all retained data after a TTL. It never captures
// user conversation messages, tool definitions, or responses.
//
// Env (all optional; capture stays off unless CPA_ERRCTX_CAPTURE=1):
//
//	CPA_ERRCTX_CAPTURE=1              enable
//	CPA_ERRCTX_TYPE=429:quota        error-type key to match (prefix match; default 429:quota)
//	CPA_ERRCTX_N=20                  stop after N samples (default 20, max 200)
//	CPA_ERRCTX_TTL_MIN=30            auto-stop and clear after minutes (default 30, max 180)
//	CPA_ERRCTX_MAX_BYTES=65536       per-sample systemInstruction cap (default 64KiB, max 256KiB)
//	CPA_ERRCTX_FINGERPRINT=Global    only capture when the folded 120-char prefix contains this
//	CPA_ERRCTX_MODEL=gemini-2.5      only capture when model contains this
const (
	errCtxDefaultN        = 20
	errCtxMaxN            = 200
	errCtxDefaultTTLMin   = 30
	errCtxMaxTTLMin       = 180
	errCtxDefaultMaxBytes = 64 << 10
	errCtxMaxMaxBytes     = 256 << 10
)

type errCtxSample struct {
	TS        string `json:"ts"`
	Finger    string `json:"fingerprint"`
	Prefix    string `json:"prefix"`
	SysPrompt string `json:"system_instruction"`
	SysLen    int    `json:"system_instruction_len"`
	Truncated bool   `json:"truncated"`
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	Tools     int    `json:"tools"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	ErrType   string `json:"error_type"`
}

type errCtxCapturer struct {
	mu          sync.Mutex
	enabled     bool
	startedAt   time.Time
	deadline    time.Time
	ttl         time.Duration
	n           int
	maxBytes    int
	matchType   string
	fpFilter    string
	modelFilter string
	samples     []errCtxSample
	seen        int // total matches observed (for visibility)
	stopped     bool
}

var errCtx = newErrCtxFromEnv()

func envInt(key string, def, max int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func newErrCtxFromEnv() *errCtxCapturer {
	c := &errCtxCapturer{}
	if os.Getenv("CPA_ERRCTX_CAPTURE") != "1" {
		return c
	}
	now := time.Now()
	c.enabled = true
	c.startedAt = now
	c.ttl = time.Duration(envInt("CPA_ERRCTX_TTL_MIN", errCtxDefaultTTLMin, errCtxMaxTTLMin)) * time.Minute
	c.deadline = now.Add(c.ttl)
	c.n = envInt("CPA_ERRCTX_N", errCtxDefaultN, errCtxMaxN)
	c.maxBytes = envInt("CPA_ERRCTX_MAX_BYTES", errCtxDefaultMaxBytes, errCtxMaxMaxBytes)
	c.matchType = strings.TrimSpace(os.Getenv("CPA_ERRCTX_TYPE"))
	if c.matchType == "" {
		c.matchType = "429:quota"
	}
	c.fpFilter = strings.TrimSpace(os.Getenv("CPA_ERRCTX_FINGERPRINT"))
	c.modelFilter = strings.TrimSpace(os.Getenv("CPA_ERRCTX_MODEL"))
	c.samples = make([]errCtxSample, 0, c.n)
	return c
}

// classifyErrType derives a bounded error-type key such as "429:quota",
// "500:other" from status + response body. Mirrors the spirit of
// logging.ErrorSampleType without importing it.
func classifyErrType(status int, body string) string {
	code := strconv.Itoa(status)
	l := strings.ToLower(body)
	switch {
	case strings.Contains(l, "resource_exhausted") || strings.Contains(l, "resource has been exhausted") || strings.Contains(l, "quota"):
		return code + ":quota"
	case strings.Contains(l, "unknown"):
		return code + ":unknown"
	default:
		return code
	}
}

func (c *errCtxCapturer) expireLocked(now time.Time) {
	if c.enabled && now.After(c.deadline) {
		c.enabled = false
		c.samples = nil
	}
}

func (c *errCtxCapturer) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	return c.enabled
}

func (c *errCtxCapturer) record(sysText, model string, stream bool, tools, status int, failed bool, latency time.Duration, body string) {
	if sysText == "" || !failed {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.expireLocked(now)
	if !c.enabled || c.stopped {
		return
	}
	et := classifyErrType(status, body)
	if c.matchType != "" && !strings.HasPrefix(et, c.matchType) && et != c.matchType {
		return
	}
	if c.modelFilter != "" && !strings.Contains(model, c.modelFilter) {
		return
	}
	prefix := foldPrefix(sysText)
	if c.fpFilter != "" && !strings.Contains(prefix, c.fpFilter) {
		return
	}
	c.seen++
	sum := sha256.Sum256([]byte(prefix))
	sys := sysText
	trunc := false
	if len(sys) > c.maxBytes {
		sys = sys[:c.maxBytes]
		trunc = true
	}
	c.samples = append(c.samples, errCtxSample{
		TS:        now.UTC().Format(time.RFC3339),
		Finger:    hex.EncodeToString(sum[:])[:12],
		Prefix:    prefix,
		SysPrompt: sys,
		SysLen:    len(sysText),
		Truncated: trunc,
		Model:     model,
		Stream:    stream,
		Tools:     tools,
		Status:    status,
		LatencyMs: latency.Milliseconds(),
		ErrType:   et,
	})
	if len(c.samples) >= c.n {
		c.stopped = true // keep captured data readable until TTL; stop adding
	}
}

func (c *errCtxCapturer) snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	out := map[string]any{"enabled": c.enabled}
	if !c.enabled {
		return out
	}
	out["started_at"] = c.startedAt.Format(time.RFC3339)
	out["expires_at"] = c.deadline.Format(time.RFC3339)
	out["ttl_minutes"] = int(c.ttl.Minutes())
	out["match_type"] = c.matchType
	out["model_filter"] = c.modelFilter
	out["fingerprint_filter"] = c.fpFilter
	out["target_n"] = c.n
	out["max_bytes"] = c.maxBytes
	out["captured"] = len(c.samples)
	out["matches_seen"] = c.seen
	out["stopped"] = c.stopped
	out["samples"] = append([]errCtxSample(nil), c.samples...)
	return out
}

// ErrorContextCaptureEnabled reports whether the opt-in capture is active.
func ErrorContextCaptureEnabled() bool { return errCtx.active() }

// RecordErrorContext records one failed Antigravity request's full
// systemInstruction when capture is enabled and the sample matches the
// configured type/model/fingerprint filters. No-op otherwise.
func RecordErrorContext(sysText, model string, stream bool, tools, status int, failed bool, latency time.Duration, body string) {
	errCtx.record(sysText, model, stream, tools, status, failed, latency, body)
}

// ErrorContextCaptureSnapshot returns the current aggregate for the endpoint.
func ErrorContextCaptureSnapshot() map[string]any { return errCtx.snapshot() }

// ReloadErrorContextCaptureFromEnvForTest re-reads the env configuration into
// the package capturer. Test-only seam; not used in production code.
func ReloadErrorContextCaptureFromEnvForTest() { errCtx = newErrCtxFromEnv() }

// DisableErrorContextCaptureForTest resets the capturer to the default-off
// state. Test-only seam.
func DisableErrorContextCaptureForTest() { errCtx = &errCtxCapturer{} }
