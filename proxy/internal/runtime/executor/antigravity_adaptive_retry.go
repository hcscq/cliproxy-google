package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	adaptiveRetryEnv          = "CPA_ANTIGRAVITY_ADAPTIVE_RETRY"
	adaptiveFake429MaxLatency = 3 * time.Second
	adaptiveFingerprintTTL    = 15 * time.Minute
	adaptiveFingerprintCap    = 4096
	adaptiveDiagPerType       = 5
	adaptiveDiagMaxBytes      = 2048
)

func adaptiveRetryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(adaptiveRetryEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// isAntigravityInstantFake429 reports the short, content-shaped RESOURCE_EXHAUSTED
// 429: generic "Resource has been exhausted" text, no Individual/reset hint,
// no ErrorInfo details, latency under the threshold, and no input tokens.
// Real quota 429s (Individual quota reached / Resets in / QUOTA_EXHAUSTED
// details) return false and keep the existing cooldown path.
func isAntigravityInstantFake429(status int, body []byte, latency time.Duration, inputTokens int) bool {
	if status != http.StatusTooManyRequests {
		return false
	}
	if latency < 0 || latency >= adaptiveFake429MaxLatency {
		return false
	}
	if inputTokens > 0 {
		return false
	}
	if !isAntigravityBare429(body) {
		return false
	}
	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	st := strings.ToUpper(gjson.GetBytes(body, "error.status").String())
	if st != "RESOURCE_EXHAUSTED" && !strings.Contains(msg, "resource has been exhausted") {
		return false
	}
	if strings.Contains(msg, "individual quota") || strings.Contains(msg, "quota reached") || strings.Contains(msg, "resets in") {
		return false
	}
	return true
}

func usageInputTokensFromBody(body []byte) int {
	for _, path := range []string{
		"usageMetadata.promptTokenCount",
		"response.usageMetadata.promptTokenCount",
		"usage.input_tokens",
		"response.usage.input_tokens",
	} {
		if v := gjson.GetBytes(body, path); v.Exists() && v.Type == gjson.Number {
			return int(v.Int())
		}
	}
	return 0
}

func sysPromptFingerprint(sys string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(sys, "\u034f", ""), "\u200b", "")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:12])
}

type adaptiveFPEntry struct {
	level int
	until time.Time
}

type adaptiveFPMem struct {
	mu   sync.Mutex
	data map[string]adaptiveFPEntry
}

var adaptiveFP = adaptiveFPMem{data: map[string]adaptiveFPEntry{}}

func adaptiveRememberLevel(fp string, level int, now time.Time) {
	if fp == "" || level < 1 {
		return
	}
	adaptiveFP.mu.Lock()
	defer adaptiveFP.mu.Unlock()
	if adaptiveFP.data == nil {
		adaptiveFP.data = map[string]adaptiveFPEntry{}
	}
	adaptiveFP.evictExpiredLocked(now)
	if len(adaptiveFP.data) >= adaptiveFingerprintCap {
		// drop the soonest-to-expire entry to stay bounded
		var victim string
		var soonest time.Time
		for k, v := range adaptiveFP.data {
			if victim == "" || v.until.Before(soonest) {
				victim, soonest = k, v.until
			}
		}
		delete(adaptiveFP.data, victim)
	}
	adaptiveFP.data[fp] = adaptiveFPEntry{level: level, until: now.Add(adaptiveFingerprintTTL)}
}

func adaptiveStartLevel(fp string, now time.Time) int {
	if fp == "" {
		return 0
	}
	adaptiveFP.mu.Lock()
	defer adaptiveFP.mu.Unlock()
	e, ok := adaptiveFP.data[fp]
	if !ok {
		return 0
	}
	if !now.Before(e.until) {
		delete(adaptiveFP.data, fp)
		return 0
	}
	if e.level < 0 {
		return 0
	}
	if e.level > adaptiveMaxLevel {
		return adaptiveMaxLevel
	}
	return e.level
}

func (s *adaptiveFPMem) evictExpiredLocked(now time.Time) {
	for k, v := range s.data {
		if !now.Before(v.until) {
			delete(s.data, k)
		}
	}
}

func resetAdaptiveFingerprintMemoryForTest() {
	adaptiveFP.mu.Lock()
	adaptiveFP.data = map[string]adaptiveFPEntry{}
	adaptiveFP.mu.Unlock()
}

// requestScopedStatusErr is a 429 (or other) that follows the prompt, not the
// credential: the conductor must not rotate to another auth or apply quota
// cooling. Only one mild failure is recorded for the request.
type requestScopedStatusErr struct {
	statusErr
}

func (e requestScopedStatusErr) IsRequestScoped() bool { return true }

func antigravityAdaptiveExhaustedErr(body []byte) requestScopedStatusErr {
	if len(body) == 0 {
		body = []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`)
	}
	return requestScopedStatusErr{statusErr: newAntigravityStatusErr(http.StatusTooManyRequests, body)}
}

type adaptiveDiagKind string

const (
	adaptiveDiagRecoveredL1 adaptiveDiagKind = "recovered_L1"
	adaptiveDiagRecoveredL2 adaptiveDiagKind = "recovered_L2"
	adaptiveDiagExhausted   adaptiveDiagKind = "exhausted"
)

type adaptiveDiagRecord struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Model       string `json:"model"`
	LatencyMS   []int  `json:"latency_ms"`
	StartLevel  int    `json:"start_level"`
	Tried       []int  `json:"tried_levels"`
	Results     []int  `json:"statuses"`
	RecoveredAt int    `json:"recovered_at,omitempty"`
	SysLen      int    `json:"sys_len"`
	HeadHash    string `json:"sys_head_hash"`
	TailHash    string `json:"sys_tail_hash"`
	HeadPreview string `json:"sys_head_preview"`
	TailPreview string `json:"sys_tail_preview"`
	At          string `json:"at"`
}

var adaptiveDiag = struct {
	mu     sync.Mutex
	byKind map[adaptiveDiagKind][]adaptiveDiagRecord
}{byKind: map[adaptiveDiagKind][]adaptiveDiagRecord{}}

func resetAdaptiveDiagForTest() {
	adaptiveDiag.mu.Lock()
	adaptiveDiag.byKind = map[adaptiveDiagKind][]adaptiveDiagRecord{}
	adaptiveDiag.mu.Unlock()
}

func recordAdaptiveDiag(rec adaptiveDiagRecord) {
	kind := adaptiveDiagKind(rec.Kind)
	if kind == "" {
		return
	}
	adaptiveDiag.mu.Lock()
	defer adaptiveDiag.mu.Unlock()
	if adaptiveDiag.byKind == nil {
		adaptiveDiag.byKind = map[adaptiveDiagKind][]adaptiveDiagRecord{}
	}
	list := adaptiveDiag.byKind[kind]
	list = append(list, rec)
	if len(list) > adaptiveDiagPerType {
		list = list[len(list)-adaptiveDiagPerType:]
	}
	adaptiveDiag.byKind[kind] = list
}

// AdaptiveRetrySnapshot is the bounded in-memory diagnostic view served by
// GET /v1/debug/adaptive-retry. It never includes user contents.
func AdaptiveRetrySnapshot() map[string]any {
	adaptiveDiag.mu.Lock()
	defer adaptiveDiag.mu.Unlock()
	out := map[string]any{
		"enabled":      adaptiveRetryEnabled(),
		"ttl":          adaptiveFingerprintTTL.String(),
		"cap_per_kind": adaptiveDiagPerType,
	}
	records := map[string][]adaptiveDiagRecord{}
	for k, list := range adaptiveDiag.byKind {
		cp := make([]adaptiveDiagRecord, len(list))
		copy(cp, list)
		records[string(k)] = cp
	}
	out["records"] = records
	adaptiveFP.mu.Lock()
	alive := 0
	now := time.Now()
	for _, e := range adaptiveFP.data {
		if now.Before(e.until) {
			alive++
		}
	}
	adaptiveFP.mu.Unlock()
	out["remembered_fingerprints"] = alive
	return out
}

func sysAdaptiveSummary(sys string) (length int, headHash, tailHash, headPrev, tailPrev string) {
	norm := strings.ReplaceAll(strings.ReplaceAll(sys, "\u034f", ""), "\u200b", "")
	length = utf8.RuneCountInString(norm)
	runes := []rune(norm)
	const window = 48
	head, tail := norm, ""
	if len(runes) > window {
		head = string(runes[:window])
		tail = string(runes[len(runes)-window:])
	} else if len(runes) > 0 {
		head = string(runes)
	}
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:6])
	}
	headHash, tailHash = sum(head), sum(tail)
	headPrev, tailPrev = truncateDiag(head, 64), truncateDiag(tail, 64)
	return
}

func truncateDiag(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func appendAdaptiveNote(reporter *helps.UsageReporter, level int, retries int) {
	if reporter == nil {
		return
	}
	note := reporter.PromptNote()
	tag := adaptiveLevelName(level)
	if note == "" {
		note = "prompt_rewritten: " + tag
	} else if !strings.Contains(note, tag) {
		note = note + "," + tag
	}
	if retries > 0 && !strings.Contains(note, "adaptive-retries=") {
		note = note + ",adaptive-retries=" + adaptiveItoa(retries)
	}
	reporter.SetPromptNote(note)
}

func adaptiveItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var neg bool
	if n < 0 {
		neg, n = true, -n
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func applyStartLevel(payload []byte, level int, hits []string) ([]byte, []string) {
	if level < 1 {
		return payload, hits
	}
	out, n := strengthenSystemObfuscation(payload, level)
	if n > 0 || level >= 1 {
		name := adaptiveLevelName(level)
		found := false
		for _, h := range hits {
			if h == name {
				found = true
				break
			}
		}
		if !found {
			hits = append(hits, name)
		}
	}
	return out, hits
}

// adaptiveRoundTrip performs one same-credential upstream attempt with payload.
// For a non-stream or failed response, live is nil and body is fully consumed.
// For a successful stream response (2xx), live is the unread *http.Response
// and body is nil — the caller must take ownership of live.
type adaptiveRoundTrip func(payload []byte) (status int, body []byte, hdr http.Header, latency time.Duration, live *http.Response, err error)

type adaptiveRetryResult struct {
	Status  int
	Body    []byte
	Header  http.Header
	Live    *http.Response
	Payload []byte
	Level   int
	Retries int
	// Exhausted is set when every adaptive attempt still produced a fake 429.
	Exhausted error
}

func runAdaptiveRetries(
	fp, model, sys string,
	startLevel int,
	payload []byte,
	firstStatus int,
	firstBody []byte,
	firstLatency time.Duration,
	rt adaptiveRoundTrip,
	reporter *helps.UsageReporter,
) adaptiveRetryResult {
	res := adaptiveRetryResult{Status: firstStatus, Body: firstBody, Payload: payload, Level: startLevel}
	tried := []int{startLevel}
	statuses := []int{firstStatus}
	latencies := []int{int(firstLatency / time.Millisecond)}
	level := startLevel
	lastLatency := firstLatency
	for level < adaptiveMaxLevel {
		next := level + 1
		up, _ := strengthenSystemObfuscation(payload, next)
		if bytes.Equal(up, payload) {
			level = next
			res.Level = next
			continue
		}
		payload = up
		status, body, hdr, latency, live, err := rt(payload)
		res.Retries++
		tried = append(tried, next)
		latencies = append(latencies, int(latency/time.Millisecond))
		if err != nil {
			// transport error: stop adaptive, let the caller see the original 429
			log.Debugf("antigravity adaptive retry: L%d transport error: %v", next, err)
			break
		}
		statuses = append(statuses, status)
		level = next
		res.Level = next
		res.Payload = payload
		res.Status = status
		res.Body = body
		res.Header = hdr
		res.Live = live
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			appendAdaptiveNote(reporter, next, res.Retries)
			adaptiveRememberLevel(fp, next, time.Now())
			kind := adaptiveDiagRecoveredL1
			if next >= 2 {
				kind = adaptiveDiagRecoveredL2
			}
			recordAdaptiveDiag(buildDiag(kind, fp, model, sys, startLevel, tried, statuses, latencies, next))
			return res
		}
		lastLatency = latency
		if !isAntigravityInstantFake429(status, body, latency, usageInputTokensFromBody(body)) {
			// a different failure (real quota, 5xx, …): stop and let the
			// existing path handle this new response
			return res
		}
		if live != nil {
			_ = live.Body.Close()
			res.Live = nil
		}
	}
	if isAntigravityInstantFake429(res.Status, res.Body, lastLatency, usageInputTokensFromBody(res.Body)) {
		appendAdaptiveNote(reporter, res.Level, res.Retries)
		recordAdaptiveDiag(buildDiag(adaptiveDiagExhausted, fp, model, sys, startLevel, tried, statuses, latencies, 0))
		res.Exhausted = antigravityAdaptiveExhaustedErr(res.Body)
	}
	return res
}

func buildDiag(kind adaptiveDiagKind, fp, model, sys string, start int, tried, statuses, latencies []int, recovered int) adaptiveDiagRecord {
	ln, hh, th, hp, tp := sysAdaptiveSummary(sys)
	rec := adaptiveDiagRecord{
		Kind:        string(kind),
		Fingerprint: fp,
		Model:       model,
		LatencyMS:   latencies,
		StartLevel:  start,
		Tried:       tried,
		Results:     statuses,
		RecoveredAt: recovered,
		SysLen:      ln,
		HeadHash:    hh,
		TailHash:    th,
		HeadPreview: hp,
		TailPreview: tp,
		At:          time.Now().In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339),
	}
	// hard cap each record
	raw := rec.HeadPreview + rec.TailPreview
	if len(raw) > adaptiveDiagMaxBytes {
		rec.HeadPreview = truncateDiag(rec.HeadPreview, 32)
		rec.TailPreview = truncateDiag(rec.TailPreview, 32)
	}
	return rec
}

func antigravityAdaptivePrep(payload []byte) (fp, sys string, start int) {
	sys = extractAntigravitySysPromptText(payload)
	fp = sysPromptFingerprint(sys)
	if adaptiveRetryEnabled() {
		start = adaptiveStartLevel(fp, time.Now())
	}
	return
}

func (e *AntigravityExecutor) maybeAdaptiveRetry(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	token, baseModel, baseURL, sessionID, alt string,
	stream, keepLive bool,
	httpClient *http.Client,
	payload []byte,
	firstStatus int,
	firstBody []byte,
	firstLatency time.Duration,
	fp, sys string,
	startLevel int,
	reporter *helps.UsageReporter,
) adaptiveRetryResult {
	res := adaptiveRetryResult{Status: firstStatus, Body: firstBody, Payload: payload, Level: startLevel}
	if e == nil || !adaptiveRetryEnabled() {
		return res
	}
	if !isAntigravityInstantFake429(firstStatus, firstBody, firstLatency, usageInputTokensFromBody(firstBody)) {
		return res
	}
	rt := func(p []byte) (int, []byte, http.Header, time.Duration, *http.Response, error) {
		httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, p, stream, alt, baseURL, sessionID)
		if errReq != nil {
			return 0, nil, nil, 0, nil, errReq
		}
		t0 := time.Now()
		resp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "generate").Do(httpReq)
		lat := time.Since(t0)
		if errDo != nil {
			return 0, nil, nil, lat, nil, errDo
		}
		if keepLive && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return resp.StatusCode, nil, resp.Header.Clone(), lat, resp, nil
		}
		body, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			return resp.StatusCode, body, resp.Header.Clone(), lat, nil, errRead
		}
		return resp.StatusCode, body, resp.Header.Clone(), lat, nil, nil
	}
	return runAdaptiveRetries(fp, baseModel, sys, startLevel, payload, firstStatus, firstBody, firstLatency, rt, reporter)
}

// shouldAdaptiveRetryStream is true only before any byte has been written to
// the downstream client. Once SSE/body bytes have been forwarded, the 429
// cannot be retried on the same response.
func shouldAdaptiveRetryStream(downstreamBytesWritten bool) bool {
	return !downstreamBytesWritten
}
