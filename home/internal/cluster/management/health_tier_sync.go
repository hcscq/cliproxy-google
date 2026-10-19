package management

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	log "github.com/sirupsen/logrus"
)

// reconcileHealthTier applies a fresh subscription tier to a stored health-check
// verdict: the "tier" (非Pro) category is dropped once the tier matches the
// expected one and added when it no longer does, exactly as a re-run of the
// check would decide. Other categories are left untouched; a record whose only
// failure was the tier becomes "pass". Running checks are not touched.
func reconcileHealthTier(rec *cluster.CredentialHealthCheck, tier, expected string) bool {
	if rec == nil || rec.Status == "running" || strings.TrimSpace(tier) == "" {
		return false
	}
	changed := false
	if rec.Tier != tier {
		rec.Tier, changed = tier, true
	}
	cats, reasons := rec.CategoryList(), rec.ReasonList()
	aligned := len(cats) == len(reasons)
	idx := -1
	for i, c := range cats {
		if c == home.HealthTier {
			idx = i
		}
	}
	match := home.HealthTierMatches(tier, expected)
	switch {
	case match && idx >= 0:
		cats = append(cats[:idx:idx], cats[idx+1:]...)
		if aligned {
			reasons = append(reasons[:idx:idx], reasons[idx+1:]...)
		} else {
			kept := reasons[:0:0]
			for _, r := range reasons {
				if !strings.HasPrefix(r, "订阅为 ") {
					kept = append(kept, r)
				}
			}
			reasons = kept
		}
		changed = true
	case !match && idx < 0:
		cats = append(cats, home.HealthTier)
		reasons = append(reasons, "订阅为 "+tier+"，期望 "+expected)
		changed = true
	case !match && idx >= 0 && aligned:
		if want := "订阅为 " + tier + "，期望 " + expected; reasons[idx] != want {
			reasons[idx], changed = want, true
		}
	}
	if !changed {
		return false
	}
	failing := false
	for _, c := range cats {
		if c != home.HealthRegionWarn {
			failing = true
		}
	}
	switch {
	case failing && rec.Status != "fail":
		rec.Status = "fail"
	case !failing && rec.Status == "fail":
		rec.Status = "pass"
		rec.RecheckCount = 0
	}
	rec.Categories = strings.Join(cats, ",")
	rec.Reasons = strings.Join(reasons, "\n")
	return true
}

// syncHealthTier is the runtime's subscription-tier hook.
func (h *Handler) syncHealthTier(ctx context.Context, authID, tier string) {
	if h == nil || h.repo == nil || h.runtime == nil {
		return
	}
	rec, ok, err := h.repo.GetHealthCheck(ctx, authID)
	if err != nil || !ok {
		return
	}
	cfg := h.runtime.ImportCheckSettings()
	before := rec.Status
	if !reconcileHealthTier(&rec, tier, cfg.ExpectedTier) {
		return
	}
	if err := h.repo.SaveHealthCheck(ctx, rec); err != nil {
		log.Warnf("health tier sync %s: %v", authID, err)
		return
	}
	log.Infof("health tier sync %s: tier=%s status %s -> %s (%s)", authID, tier, before, rec.Status, rec.Categories)
	if before != rec.Status && cfg.BlockFailed {
		h.runtime.ReserveChanged(ctx)
	}
}
