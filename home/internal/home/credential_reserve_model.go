package home

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

// ModelCapacityRow is one credential able to list a model-rule model.
type ModelCapacityRow struct {
	ID        string  `gorm:"column:id"`
	Reserved  bool    `gorm:"column:reserved"`
	Usable    bool    `gorm:"column:usable"`    // some matching model is not exhausted/cooling
	Remaining float64 `gorm:"column:remaining"` // best remaining fraction among usable matches
}

// ReserveModelStore is the optional model-aware part of ReserveStore.
type ReserveModelStore interface {
	ReserveModelCapacity(ctx context.Context, pattern string) ([]ModelCapacityRow, error)
}

// ReserveModelStat is the panel view of one model rule.
type ReserveModelStat struct {
	Pattern  string `json:"pattern"`
	Min      int    `json:"min"`
	Usable   int    `json:"usable"`   // non-reserved credentials able to serve it now
	Reserves int    `json:"reserves"` // eligible reserves able to serve it now
	Short    bool   `json:"short"`
	Since    int64  `json:"shortfall_since,omitempty"`
}

// GlobToLike converts a * glob into a LIKE pattern (escape \) and returns the literal
// prefix before the first *.
func GlobToLike(glob string) (like, prefix string) {
	glob = strings.TrimSpace(glob)
	if i := strings.Index(glob, "*"); i >= 0 {
		prefix = glob[:i]
	} else {
		prefix = glob + `"`
	}
	var b strings.Builder
	for _, ch := range glob {
		switch ch {
		case '*':
			b.WriteByte('%')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(ch)
		default:
			b.WriteRune(ch)
		}
	}
	return b.String(), prefix
}

type modelRuleEval struct {
	rule   config.ReserveModelRule
	usable int
	cands  []ReserveCandidate // eligible, usable reserves for this rule, best first
}

// evalModelRules computes usable counts and rule-capable eligible reserves.
// eligible is the already-filtered/ordered generic candidate list.
func evalModelRules(ctx context.Context, s ReserveStore, rules []config.ReserveModelRule, eligible []ReserveCandidate) []modelRuleEval {
	ms, ok := s.(ReserveModelStore)
	if !ok || len(rules) == 0 {
		return nil
	}
	byID := make(map[string]ReserveCandidate, len(eligible))
	for _, c := range eligible {
		byID[c.ID] = c
	}
	out := make([]modelRuleEval, 0, len(rules))
	for _, rule := range rules {
		rows, err := ms.ReserveModelCapacity(ctx, rule.Pattern)
		if err != nil {
			continue
		}
		ev := modelRuleEval{rule: rule}
		rem := map[string]float64{}
		for _, row := range rows {
			if !row.Usable {
				continue
			}
			if !row.Reserved {
				ev.usable++
				continue
			}
			if c, ok := byID[row.ID]; ok && c.Healthy {
				ev.cands = append(ev.cands, c)
				rem[row.ID] = row.Remaining
			}
		}
		sort.SliceStable(ev.cands, func(i, j int) bool {
			a, b := ev.cands[i], ev.cands[j]
			if rem[a.ID] != rem[b.ID] {
				return rem[a.ID] > rem[b.ID]
			}
			if a.Seq != b.Seq && a.Seq > 0 && b.Seq > 0 {
				return a.Seq < b.Seq
			}
			return a.ID < b.ID
		})
		out = append(out, ev)
	}
	return out
}

func (r *Runtime) modelShortSince(pattern string, short bool, now time.Time) time.Time {
	if r.reserve.modelSince == nil {
		r.reserve.modelSince = map[string]time.Time{}
	}
	if !short {
		delete(r.reserve.modelSince, pattern)
		return time.Time{}
	}
	t, ok := r.reserve.modelSince[pattern]
	if !ok {
		t = now
		r.reserve.modelSince[pattern] = t
	}
	return t
}
