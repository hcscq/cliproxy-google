package home

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Reserve ban probe (储备封号探测): reserved credentials never serve requests, so a 403
// ban (terms of service / suspended / verification) applied while in reserve was only
// discovered by the first real request after promotion. The leader now sends each
// reserved credential one minimal generateContent (maxOutputTokens 1) through its pinned
// egress every routing.reserve.ban-probe-interval (jittered per credential, budgeted
// per tick so 10k reserves spread evenly), and right before promotion
// (routing.reserve.pre-promote-check). A 403 is classified exactly like live requests
// (classifyAccountBlock) and disables the credential via the account block guard, so it
// leaves the candidate list and is audited as a ban while reserved. Anything else
// (429, 5xx, network) is not a ban and does not block promotion. No egress = no probe.

const (
	reserveProbeTickEvery   = 30 * time.Second
	reserveProbeMaxPerTick  = 30
	reserveProbeConcurrency = 4
	reserveProbeFresh       = 15 * time.Minute
	reservePrePromoteExtra  = 3
	reserveProbeKV          = "reserve-ban-probe:"
)

type reserveProbeResult struct {
	At     time.Time `json:"at"`
	Status int       `json:"http_status"`
	Result string    `json:"result"` // ok | banned | skipped | error
	Detail string    `json:"detail,omitempty"`
}

type reserveProber struct {
	mu   sync.Mutex
	last map[string]reserveProbeResult
}

func (p *reserveProber) get(id string) (reserveProbeResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.last[id]
	return v, ok
}

func (p *reserveProber) set(id string, v reserveProbeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]reserveProbeResult{}
	}
	p.last[id] = v
}

// prune drops ids no longer reserved.
func (p *reserveProber) prune(keep map[string]time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.last {
		if _, ok := keep[id]; !ok {
			delete(p.last, id)
		}
	}
}

// reserveProbeJitter spreads first probes of each credential over the interval.
func reserveProbeJitter(id string, every time.Duration) time.Duration {
	if every <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return time.Duration(uint64(h.Sum32()) % uint64(every))
}

func reserveProbeModel(id string) string {
	for _, info := range registry.GetGlobalRegistry().GetModelsForClient(id) {
		if info != nil && antigravityModelBucket(info.ID) == quotaBucketGemini {
			return info.ID
		}
	}
	return "gemini-2.5-flash"
}

// probeReserveBan sends one minimal request and disables the credential on an
// account-level 403. Returns the result (also cached and mirrored to KV for 48h).
func (r *Runtime) probeReserveBan(ctx context.Context, id string, now time.Time) reserveProbeResult {
	res := reserveProbeResult{At: now, Result: "error"}
	pctx, cancel := context.WithTimeout(ctx, accountBlockProbeTimeout)
	defer cancel()
	full := r.fullAuth(pctx, id)
	switch {
	case full == nil:
		res.Result, res.Detail = "skipped", "not found"
	case full.Disabled:
		res.Result, res.Detail = "skipped", "disabled"
	case !strings.EqualFold(full.Provider, "antigravity"):
		res.Result, res.Detail = "skipped", "provider "+full.Provider
	case strings.TrimSpace(full.ProxyURL) == "":
		res.Result, res.Detail = "skipped", "no pinned egress" // never via node IP
	case extractAccessToken(full) == "":
		res.Result, res.Detail = "skipped", "no access token"
	}
	if res.Result != "skipped" {
		model := reserveProbeModel(id)
		started := time.Now()
		code, _, body, err := sendQuotaProbe(pctx, full, model)
		r.recordHealthCheckUsage(full, model, started, code, body, err)
		res.Status = code
		switch {
		case err != nil:
			res.Detail = clip(err.Error(), 200)
		case code == http.StatusForbidden:
			if c, label := classifyAccountBlock(code, string(body)); c != "" {
				res.Result, res.Detail = "banned", label
				f := accountBlockFinding{code: c, label: label, upstream: upstreamMessage(string(body)), model: model, at: now}
				r.disableForAccountBlock(pctx, id, f, now)
			} else {
				res.Detail = upstreamMessage(string(body))
			}
		case code >= 200 && code < 300 || code == http.StatusTooManyRequests:
			res.Result = "ok"
		default:
			res.Detail = http.StatusText(code) + ": " + upstreamMessage(string(body))
		}
	}
	r.reserveProbe.set(id, res)
	if raw, err := json.Marshal(res); err == nil {
		_, _ = r.KVSet(pctx, reserveProbeKV+id, raw, 48*time.Hour, "")
	}
	if res.Result == "banned" {
		log.Warnf("credential reserve: ban probe %s -> %d %s (disabled, will not be promoted)", id, res.Status, res.Detail)
	} else {
		log.Debugf("credential reserve: ban probe %s -> %d %s %s", id, res.Status, res.Result, res.Detail)
	}
	return res
}

// probeReserveBatch probes ids with bounded concurrency.
func (r *Runtime) probeReserveBatch(ctx context.Context, ids []string, now time.Time) map[string]reserveProbeResult {
	out := make(map[string]reserveProbeResult, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, reserveProbeConcurrency)
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			v := r.probeReserveBan(ctx, id, now)
			mu.Lock()
			out[id] = v
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}

// reserveBanProbeTick (leader) probes the reserved credentials that are due, at most
// a per-tick budget sized so every reserve is covered once per interval.
func (r *Runtime) reserveBanProbeTick(ctx context.Context, now time.Time) {
	every := r.reserveSettings().BanProbeEvery
	if every <= 0 || !r.reserveProbing.CompareAndSwap(false, true) {
		return
	}
	defer r.reserveProbing.Store(false)
	reserved := r.ReservedSince()
	r.reserveProbe.prune(reserved)
	if len(reserved) == 0 {
		return
	}
	ticks := int(every / reserveProbeTickEvery)
	if ticks < 1 {
		ticks = 1
	}
	budget := len(reserved)/ticks + 2
	if budget > reserveProbeMaxPerTick {
		budget = reserveProbeMaxPerTick
	}
	var due []string
	for id, since := range reserved {
		last, ok := r.reserveProbe.get(id)
		var next time.Time
		if ok {
			next = last.At.Add(every)
		} else {
			// First sight on this leader: spread over the interval from reserved_since
			// (or now), but never later than one interval from now.
			base := since
			if base.IsZero() || now.Sub(base) > every {
				base = now.Add(-every)
			}
			next = base.Add(reserveProbeJitter(id, every))
			if !next.After(now.Add(-every)) {
				next = now
			}
		}
		if !next.After(now) {
			due = append(due, id)
			if len(due) >= budget {
				break
			}
		}
	}
	if len(due) == 0 {
		return
	}
	res := r.probeReserveBatch(ctx, due, now)
	banned := 0
	for _, v := range res {
		if v.Result == "banned" {
			banned++
		}
	}
	if banned > 0 {
		log.Warnf("credential reserve: ban probe found %d banned of %d probed", banned, len(due))
		r.syncReserve(ctx)
	}
}

func (r *Runtime) startReserveBanProbe(ctx context.Context) {
	go func() {
		t := time.NewTicker(reserveProbeTickEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if r.coreManager != nil && r.catalogLeader() {
				r.reserveBanProbeTick(ctx, time.Now().UTC())
			}
		}
	}()
}

// prePromoteFilter returns up to n candidates (in order) not found banned, probing
// those without a fresh result; at most n+reservePrePromoteExtra probes per call.
func (r *Runtime) prePromoteFilter(ctx context.Context, cands []ReserveCandidate, n int, now time.Time) ([]ReserveCandidate, int) {
	if r.coreManager == nil {
		if n > len(cands) {
			n = len(cands)
		}
		return cands[:n], 0
	}
	out := make([]ReserveCandidate, 0, n)
	banned := 0
	limit := n + reservePrePromoteExtra
	i := 0
	for len(out) < n && i < len(cands) && limit > 0 {
		want := n - len(out)
		var batch []ReserveCandidate
		var toProbe []string
		for ; i < len(cands) && len(batch) < want; i++ {
			c := cands[i]
			batch = append(batch, c)
			if v, ok := r.reserveProbe.get(c.ID); ok && now.Sub(v.At) < reserveProbeFresh && v.Result != "banned" {
				continue
			}
			toProbe = append(toProbe, c.ID)
		}
		if len(toProbe) > limit {
			toProbe = toProbe[:limit]
			// Candidates beyond the probe limit are not promoted this round.
			keep := map[string]bool{}
			for _, id := range toProbe {
				keep[id] = true
			}
			var trimmed []ReserveCandidate
			for _, c := range batch {
				if v, ok := r.reserveProbe.get(c.ID); keep[c.ID] || ok && now.Sub(v.At) < reserveProbeFresh {
					trimmed = append(trimmed, c)
				}
			}
			batch = trimmed
		}
		limit -= len(toProbe)
		res := r.probeReserveBatch(ctx, toProbe, now)
		for _, c := range batch {
			v, ok := res[c.ID]
			if !ok {
				v, _ = r.reserveProbe.get(c.ID)
			}
			if v.Result == "banned" || v.Result == "skipped" && v.Detail == "disabled" {
				banned++
				continue
			}
			out = append(out, c)
		}
	}
	return out, banned
}
