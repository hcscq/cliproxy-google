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
	total     int
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
			StreamTrue: e.StreamTrue, StreamFalse: e.StreamFalse, Models: e.Models}
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
	return out
}

// SysPromptSampleEnabled reports whether the opt-in sampler is currently active.
func SysPromptSampleEnabled() bool { return sysSampler.active() }

// RecordSysPromptSample records one Antigravity request outcome. latency is only
// used for 429 average latency. It is a no-op when the sampler is disabled.
func RecordSysPromptSample(sysText, model string, stream bool, tools, statusCode int, failed bool, latency time.Duration) {
	if sysText == "" {
		return
	}
	sysSampler.mu.Lock()
	if !sysSampler.enabled {
		sysSampler.mu.Unlock()
		return
	}
	sysSampler.mu.Unlock()
	// record (re-checks enablement/TTL under lock)
	sysSampler.recordWithLatency(sysText, model, stream, tools, statusCode, failed, latency)
}

func (s *sysPromptSampler) recordWithLatency(sysText, model string, stream bool, tools, statusCode int, failed bool, latency time.Duration) {
	s.record(sysText, model, stream, tools, statusCode, failed)
	if statusCode != 429 || latency <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	prefix := foldPrefix(sysText)
	sum := sha256.Sum256([]byte(prefix))
	key := hex.EncodeToString(sum[:])[:12]
	if e := s.fps[key]; e != nil {
		e.Sum429Millis += latency.Milliseconds()
	} else if s.other != nil {
		s.other.Sum429Millis += latency.Milliseconds()
	}
}

// SysPromptSampleSnapshot returns the current aggregate for the mgmt endpoint.
func SysPromptSampleSnapshot() map[string]any { return sysSampler.snapshot() }
