package home

import (
	"context"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Ultra spread: at most UltraLimit() Ultra-tier credentials per egress IP.
//
// The tier is only known after onboarding (loadCodeAssist through the credential's
// own egress), so placement happens in the egress round once a NEW credential
// (created within ultraPlaceWindow) is first seen as Ultra:
//   - its egress still has room -> it stays (marked placed);
//   - otherwise it is moved once to the healthy, assignable egress with the fewest
//     Ultra credentials (then fewest credentials) that has room;
//   - no egress has room -> it stays where it is, keeps serving, and gets a
//     visible note (egress_ultra_note); it is retried each round until placed or
//     the window ends. It is never paused for this and never stacked silently.
// Older credentials are never moved automatically (IP stability); violations are
// listed by UltraEgressReport and fixed by the explicit rebalance action.

const (
	EgressUltraPlacedKey = "egress_ultra_placed"
	EgressUltraNoteKey   = "egress_ultra_note"
	ultraPlaceWindow     = 48 * time.Hour
)

func isUltraTier(tier string) bool { return strings.Contains(strings.ToLower(tier), "ultra") }

type ultraCand struct {
	id    string
	proxy string
}

type ultraDecision struct {
	id   string
	to   EgressSpec // zero ProxyURL = stay
	note string     // set when no egress had room
}

// planUltraPlacement decides each candidate in order (oldest first). ultraCounts /
// counts are updated in place to reflect the decisions.
func planUltraPlacement(cands []ultraCand, specs []EgressSpec, status map[string]*EgressStatus, ultraCounts, counts map[string]int, limit, maxPer int, allowPrimary bool) []ultraDecision {
	out := make([]ultraDecision, 0, len(cands))
	for _, c := range cands {
		if limit <= 0 || ultraCounts[c.proxy] < limit {
			ultraCounts[c.proxy]++
			out = append(out, ultraDecision{id: c.id})
			continue
		}
		var best EgressSpec
		found := false
		for _, s := range specs {
			if s.ProxyURL == c.proxy || s.Removed || (s.Primary && !allowPrimary) {
				continue
			}
			if st := status[s.ProxyURL]; st == nil || !st.Healthy {
				continue
			}
			if ultraCounts[s.ProxyURL] >= limit || (maxPer > 0 && counts[s.ProxyURL] >= maxPer) {
				continue
			}
			if !found || ultraCounts[s.ProxyURL] < ultraCounts[best.ProxyURL] ||
				(ultraCounts[s.ProxyURL] == ultraCounts[best.ProxyURL] && (counts[s.ProxyURL] < counts[best.ProxyURL] ||
					(counts[s.ProxyURL] == counts[best.ProxyURL] && s.Name < best.Name))) {
				best, found = s, true
			}
		}
		if !found {
			out = append(out, ultraDecision{id: c.id, note: "无空闲出口：每 IP Ultra 上限已满，暂留原出口"})
			continue
		}
		ultraCounts[best.ProxyURL]++
		counts[best.ProxyURL]++
		counts[c.proxy]--
		out = append(out, ultraDecision{id: c.id, to: best})
	}
	return out
}

// placeNewUltras runs in the egress round (leader) after pins are known.
func (r *Runtime) placeNewUltras(ctx context.Context, specs []EgressSpec, status map[string]*EgressStatus, pins map[string]egressPin, counts map[string]int, settings EgressSettingsValue, now time.Time) {
	known := map[string]bool{}
	for _, s := range specs {
		known[s.ProxyURL] = true
	}
	ultraCounts := map[string]int{}
	type cand struct {
		ultraCand
		created time.Time
	}
	var cands []cand
	for id, p := range pins {
		if !p.ultra || p.proxy == "" {
			continue
		}
		if p.placed || p.paused || !known[p.proxy] || p.created.IsZero() || now.Sub(p.created) > ultraPlaceWindow {
			ultraCounts[p.proxy]++
			continue
		}
		cands = append(cands, cand{ultraCand{id, p.proxy}, p.created})
	}
	if len(cands) == 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].created.Equal(cands[j].created) {
			return cands[i].created.Before(cands[j].created)
		}
		return cands[i].id < cands[j].id
	})
	list := make([]ultraCand, len(cands))
	for i, c := range cands {
		list[i] = c.ultraCand
	}
	cp := make(map[string]int, len(counts))
	for k, v := range counts {
		cp[k] = v
	}
	for _, d := range planUltraPlacement(list, specs, status, ultraCounts, cp, settings.UltraLimit(), settings.MaxPerEgress, settings.AllowPrimary) {
		full := r.fullAuth(ctx, d.id)
		if full == nil || full.Metadata == nil {
			continue
		}
		from := strings.TrimSpace(full.ProxyURL)
		if from != pins[d.id].proxy {
			continue // changed underneath; next round decides
		}
		if d.note != "" {
			if metaString(full, EgressUltraNoteKey) == d.note {
				continue
			}
			full.Metadata[EgressUltraNoteKey] = d.note
		} else {
			if d.to.ProxyURL != "" {
				ApplyEgressMetadata(full, d.to)
			}
			full.Metadata[EgressUltraPlacedKey] = now.Format(time.RFC3339)
			delete(full.Metadata, EgressUltraNoteKey)
		}
		full.UpdatedAt = now
		if _, err := r.coreManager.Update(ctx, full); err != nil {
			log.WithError(err).Warnf("egress ultra: update %s failed", d.id)
			continue
		}
		p := pins[d.id]
		switch {
		case d.note != "":
			log.Warnf("egress ultra: %s (%s) stays on %s: %s", d.id, full.Label, from, d.note)
		case d.to.ProxyURL != "":
			log.Infof("egress ultra: %s (%s) moved %s -> %s (%s)", d.id, full.Label, from, d.to.Name, d.to.IP)
			p.proxy = d.to.ProxyURL
			counts[from]--
			counts[d.to.ProxyURL]++
			p.placed = true
		default:
			p.placed = true
		}
		r.setPin(d.id, p)
	}
}
