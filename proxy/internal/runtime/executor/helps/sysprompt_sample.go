package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bounded, in-memory, opt-in sampler that correlates the leading text of an
// Antigravity systemInstruction with the request outcome (bare 429 vs success).
// It exists only to locate which client system prompts trigger upstream's
// instant 429 during an incident. It NEVER stores user messages, tool
// definitions, response content or the full system prompt, NEVER writes to
// disk or a database, is capped in size, and self-destructs after a TTL.
//
// Enable with CPA_SYSPROMPT_SAMPLE=1 (default off). TTL minutes come from
// CPA_SYSPROMPT_SAMPLE_TTL_MIN (default 90); after it elapses the sampler
// stops recording and drops all retained data on the next touch.

const (
	sysSamplePrefixRunes   = 120
	sysSampleMaxFinger     = 80
	sysSampleDefaultTTLMin = 90
)

// sysSampleCandidates are substrings checked against the full systemInstruction
// text to tally how known coding-agent identity markers correlate with 429s.
// They are agent/product markers only, never user content.
var sysSampleCandidates = []string{
	"Claude Code", "Codex", "GPT-5", "Anthropic", "opencode",
	"Cline", "Roo", "Cursor", "aider", "Kilo", "Gemini CLI", "Qwen",
}

type sysSampleEntry struct {
	Prefix       string
	Total        int
	C429         int
	CSuccess     int
	COther       int
	Sum429Millis int64
	Instant429   int
	Input0_429   int
	MaxLen       int
	MaxTools     int
	StreamTrue   int
	StreamFalse  int
	Models       map[string]int
}

type sysSampleCand struct {
	Contains int
	C429     int
	CSuccess int
}

type sysPromptSampler struct {
	mu        sync.Mutex
	enabled   bool
	startedAt time.Time
	deadline  time.Time
	ttl       time.Duration
	fps       map[string]*sysSampleEntry
	other     *sysSampleEntry
	cands     map[string]*sysSampleCand
	rules     map[string]*ruleStat
	total     int
	instant0  int // 429 that were instant (<1s) AND input_tokens==0
	instantN  int // all 429 counted for the instant0 ratio denominator
}

// ruleStat tracks, per prompt-rewrite rule name, how its rewritten requests
// fared downstream: still 429 (deep trigger remains) vs 200 (recovered).
type ruleStat struct {
	RewrittenTotal int
	Still429       int
	OK             int
}

var sysSampler = newSysPromptSamplerFromEnv()

func newSysPromptSamplerFromEnv() *sysPromptSampler {
	s := &sysPromptSampler{}
	if os.Getenv("CPA_SYSPROMPT_SAMPLE") != "1" {
		return s
	}
	ttlMin := sysSampleDefaultTTLMin
	if v := strings.TrimSpace(os.Getenv("CPA_SYSPROMPT_SAMPLE_TTL_MIN")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ttlMin = n
		}
	}
	now := time.Now()
	s.enabled = true
	s.startedAt = now
	s.ttl = time.Duration(ttlMin) * time.Minute
	s.deadline = now.Add(s.ttl)
	s.fps = make(map[string]*sysSampleEntry)
	s.cands = make(map[string]*sysSampleCand)
	s.rules = make(map[string]*ruleStat)
	return s
}

// expireLocked stops and clears the sampler once past its deadline. Caller holds mu.
func (s *sysPromptSampler) expireLocked(now time.Time) {
	if !s.enabled {
		return
	}
	if now.After(s.deadline) {
		s.enabled = false
		s.fps = nil
		s.other = nil
		s.cands = nil
		s.rules = nil
	}
}

func (s *sysPromptSampler) active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	return s.enabled
}

// foldPrefix collapses all whitespace runs to single spaces, trims, and keeps
// the first sysSamplePrefixRunes runes.
func foldPrefix(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	r := []rune(collapsed)
	if len(r) > sysSamplePrefixRunes {
		r = r[:sysSamplePrefixRunes]
	}
	return string(r)
}

func (s *sysPromptSampler) record(sysText, model string, stream bool, tools, statusCode int, failed bool) {
	s.recordFull(sysText, model, stream, tools, statusCode, failed, 0, 0, "")
}

func (s *sysPromptSampler) recordFull(sysText, model string, stream bool, tools, statusCode int, failed bool, latency time.Duration, inputTokens int64, rewriteNote string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	if !s.enabled {
		return
	}
	is429 := statusCode == 429
	success := !failed && statusCode >= 200 && statusCode < 300
	s.total++

	prefix := foldPrefix(sysText)
	sum := sha256.Sum256([]byte(prefix))
	key := hex.EncodeToString(sum[:])[:12]

	entry := s.fps[key]
	if entry == nil {
		if len(s.fps) >= sysSampleMaxFinger {
			if s.other == nil {
				s.other = &sysSampleEntry{Prefix: "(other)", Models: map[string]int{}}
			}
			entry = s.other
		} else {
			entry = &sysSampleEntry{Prefix: prefix, Models: map[string]int{}}
			s.fps[key] = entry
		}
	}
	entry.Total++
	if is429 {
		entry.C429++
	} else if success {
		entry.CSuccess++
	} else {
		entry.COther++
	}
	if l := len([]rune(sysText)); l > entry.MaxLen {
		entry.MaxLen = l
	}
	if tools > entry.MaxTools {
		entry.MaxTools = tools
	}
	if stream {
		entry.StreamTrue++
	} else {
		entry.StreamFalse++
	}
	if model != "" && entry.Models != nil && len(entry.Models) < 16 {
		entry.Models[model]++
	}

	if is429 {
		s.instantN++
		if latency > 0 {
			entry.Sum429Millis += latency.Milliseconds()
		}
		if latency > 0 && latency < time.Second && inputTokens == 0 {
			entry.Instant429++
			s.instant0++
		}
		if inputTokens == 0 {
			entry.Input0_429++
		}
	}
	for _, rn := range parseRewrittenRules(rewriteNote) {
		rs := s.rules[rn]
		if rs == nil {
			rs = &ruleStat{}
			s.rules[rn] = rs
		}
		rs.RewrittenTotal++
		if is429 {
			rs.Still429++
		} else if success {
			rs.OK++
		}
	}

	for _, cand := range sysSampleCandidates {
		if !strings.Contains(sysText, cand) {
			continue
		}
		c := s.cands[cand]
		if c == nil {
			c = &sysSampleCand{}
			s.cands[cand] = c
		}
		c.Contains++
		if is429 {
			c.C429++
		} else if success {
			c.CSuccess++
		}
	}
}

type fpOut struct {
	Fingerprint  string         `json:"fingerprint"`
	Prefix       string         `json:"prefix"`
	Total        int            `json:"total"`
	C429         int            `json:"c429"`
	CSuccess     int            `json:"c_success"`
	COther       int            `json:"c_other"`
	Rate429Pct   float64        `json:"rate_429_pct"`
	Avg429Millis int64          `json:"avg_429_ms"`
	Instant429   int            `json:"instant_429"`
	Input0_429   int            `json:"input0_429"`
	MaxLen       int            `json:"max_sysprompt_len"`
	MaxTools     int            `json:"max_tools"`
	StreamTrue   int            `json:"stream_true"`
	StreamFalse  int            `json:"stream_false"`
	Models       map[string]int `json:"models"`
}

// snapshot returns a JSON-serializable aggregate for the management endpoint.
func (s *sysPromptSampler) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	out := map[string]any{"enabled": s.enabled}
	if !s.enabled {
		if s.total > 0 && s.fps == nil {
			out["note"] = "expired and cleared"
		}
		return out
	}
	out["started_at"] = s.startedAt.Format(time.RFC3339)
	out["expires_at"] = s.deadline.Format(time.RFC3339)
	out["ttl_minutes"] = int(s.ttl.Minutes())
	out["total_requests"] = s.total
	out["max_fingerprints"] = sysSampleMaxFinger

	mk := func(key string, e *sysSampleEntry) fpOut {
		o := fpOut{Fingerprint: key, Prefix: e.Prefix, Total: e.Total, C429: e.C429,
			CSuccess: e.CSuccess, COther: e.COther, MaxLen: e.MaxLen, MaxTools: e.MaxTools,
			StreamTrue: e.StreamTrue, StreamFalse: e.StreamFalse, Models: e.Models,
			Instant429: e.Instant429, Input0_429: e.Input0_429}
		if e.Total > 0 {
			o.Rate429Pct = float64(int(1000*float64(e.C429)/float64(e.Total))) / 10
		}
		if e.C429 > 0 {
			o.Avg429Millis = e.Sum429Millis / int64(e.C429)
		}
		return o
	}
	fps := make([]fpOut, 0, len(s.fps))
	for k, e := range s.fps {
		fps = append(fps, mk(k, e))
	}
	sort.Slice(fps, func(i, j int) bool {
		if fps[i].C429 != fps[j].C429 {
			return fps[i].C429 > fps[j].C429
		}
		return fps[i].Total > fps[j].Total
	})
	out["fingerprints"] = fps
	if s.other != nil {
		out["other"] = mk("(other)", s.other)
	}
	cands := map[string]map[string]int{}
	for name, c := range s.cands {
		cands[name] = map[string]int{"contains": c.Contains, "c429": c.C429, "c_success": c.CSuccess}
	}
	out["candidates"] = cands
	// (a) instant (<1s) 429 with input_tokens==0 as a share of all 429
	out["instant_input0_429"] = s.instant0
	out["total_429"] = s.instantN
	if s.instantN > 0 {
		out["instant_input0_429_pct"] = float64(int(1000*float64(s.instant0)/float64(s.instantN))) / 10
	}
	// (d) per prompt-rewrite rule: did rewritten requests recover (200) or still 429?
	rules := map[string]map[string]any{}
	for name, rs := range s.rules {
		m := map[string]any{"rewritten_total": rs.RewrittenTotal, "still_429": rs.Still429, "ok": rs.OK}
		if rs.RewrittenTotal > 0 {
			m["success_pct"] = float64(int(1000*float64(rs.OK)/float64(rs.RewrittenTotal))) / 10
		}
		rules[name] = m
	}
	out["rule_stats"] = rules
	return out
}

// SysPromptSampleEnabled reports whether the opt-in sampler is currently active.
func SysPromptSampleEnabled() bool { return sysSampler.active() }

// RecordSysPromptSample records one Antigravity request outcome. latency is only
// used for 429 average latency. It is a no-op when the sampler is disabled.
func RecordSysPromptSample(sysText, model string, stream bool, tools, statusCode int, failed bool, latency time.Duration, inputTokens int64, rewriteNote string) {
	if sysText == "" {
		return
	}
	sysSampler.recordFull(sysText, model, stream, tools, statusCode, failed, latency, inputTokens, rewriteNote)
}

func (s *sysPromptSampler) recordWithLatency(sysText, model string, stream bool, tools, statusCode int, failed bool, latency time.Duration) {
	s.recordFull(sysText, model, stream, tools, statusCode, failed, latency, 0, "")
}

// parseRewrittenRules extracts rule names from a "prompt_rewritten: a,b,c" note.
// A detect-only "prompt_flagged:" note is ignored (no rewrite happened).
func parseRewrittenRules(note string) []string {
	const pfx = "prompt_rewritten: "
	i := strings.Index(note, pfx)
	if i < 0 {
		return nil
	}
	rest := note[i+len(pfx):]
	if j := strings.IndexByte(rest, '|'); j >= 0 {
		rest = rest[:j]
	}
	var out []string
	for _, r := range strings.Split(rest, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// SysPromptSampleSnapshot returns the current aggregate for the mgmt endpoint.
func SysPromptSampleSnapshot() map[string]any { return sysSampler.snapshot() }
