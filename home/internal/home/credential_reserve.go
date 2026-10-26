package home

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	log "github.com/sirupsen/logrus"
)

// Credential reserve (凭据储备). Reserved credentials stay enabled, so every background
// loop (token refresh, quota fetch, subscription tier refresh, catalog, liveness) and
// egress pinning keeps applying; only request scheduling skips them (coreauth reserve
// set). State lives in the credential_reserve table (never auth metadata). Every Home
// mirrors the set every reserveSyncEvery and immediately on a quota-sync "reserve"
// broadcast. The leader Home promotes reserves when the active count stays below the
// configured minimum.
//
// Active count = available credentials: enabled (bans/egress or error pauses disable),
// not reserved, not in error, with a refresh token and not failed by the import
// health check. Wholly quota-cooled credentials do not count by default
// (routing.reserve.count-cooled=false); per-model cooldowns never remove a credential.
// Model rules (routing.reserve.model-rules) also keep a minimum of usable credentials
// per model pattern, e.g. gemini-4-argon-*:6.

const (
	reserveSyncEvery    = 10 * time.Second
	reserveControlEvery = 30 * time.Second
)

// ReserveCandidate is a reserved credential with its promotion-order inputs.
type ReserveCandidate struct {
	ID        string    `gorm:"column:id"`
	Seq       int64     `gorm:"column:seq"`
	CreatedAt time.Time `gorm:"column:created_at"`
	Tier      string    `gorm:"column:tier"`
	Healthy   bool      `gorm:"column:healthy"`
	Health    string    `gorm:"column:health"` // import health check: pass | fail | running | "" (unchecked)
}

// ReserveStats is the panel summary.
type ReserveStats struct {
	Reserved           int  `json:"reserved"`
	EverReserved       int  `json:"ever_reserved"`
	BansReserved       int  `json:"bans_reserved"`        // banned while in reserve
	BansActive         int  `json:"bans_active"`          // banned while schedulable
	BansAfterPromotion int  `json:"bans_after_promotion"` // subset of bans_active: had been reserved before
	Promotions24h      int  `json:"promotions_24h"`
	MinActive          int  `json:"min_active"`
	Active             int  `json:"active"`
	Eligible           int  `json:"eligible"` // reserves matching the tier filter (-1 = not checked)
	Short              bool `json:"short"`    // active < min and no eligible reserve
	// Enabled / Usable exclude Free-tier credentials (as the promoter counts):
	// enabled = schedulable and not in reserve; usable = enabled and not cooling.
	// Active is whichever of the two the promoter compares with MinActive.
	Enabled            int                `json:"enabled"`
	Usable             int                `json:"usable"`
	CountCooled        bool               `json:"count_cooled"`
	LastPromotionUnix  int64              `json:"last_promotion,omitempty"`
	ShortfallSinceUnix int64              `json:"shortfall_since,omitempty"`
	ModelRules         []ReserveModelStat `json:"model_rules,omitempty"`
}

// ReserveStore is the persistence used by the reserve controller.
type ReserveStore interface {
	ListReservedCredentials(ctx context.Context) (map[string]time.Time, error)
	SetCredentialsReserved(ctx context.Context, ids []string, reserved bool, kind, detail string) (int, error)
	ListReserveCandidates(ctx context.Context) ([]ReserveCandidate, error)
	CountActiveCredentials(ctx context.Context, countCooled bool) (int, error)
	CountReserveEvents(ctx context.Context, kind string, since time.Time) (int, error)
	RecordReserveBans(ctx context.Context) (int, error)
}

type credentialReserve struct {
	since          atomic.Value // map[string]time.Time
	mu             sync.Mutex
	shortfallSince time.Time
	lastActive     atomic.Int64
	state          string               // last logged shortfall state (ok|waiting|limited|empty|promoted)
	eligible       atomic.Int64         // eligible reserves at the last check (-1 = unknown)
	modelSince     map[string]time.Time // model rule pattern -> shortfall start (guarded by mu)
}

func (r *Runtime) reserveStore() ReserveStore {
	if r != nil && r.reserveStoreOverride != nil {
		return r.reserveStoreOverride
	}
	if r == nil || r.clusterAdapter == nil || !r.clusterAdapter.Enabled() {
		return nil
	}
	s, _ := r.clusterAdapter.(ReserveStore)
	return s
}

// ReservedSince returns reserved credential -> reserved_since (last sync).
func (r *Runtime) ReservedSince() map[string]time.Time {
	m, _ := r.reserve.since.Load().(map[string]time.Time)
	return m
}

func (r *Runtime) syncReserve(ctx context.Context) {
	s := r.reserveStore()
	if s == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m, err := s.ListReservedCredentials(cctx)
	if err != nil {
		log.WithError(err).Debug("credential reserve: sync failed; keeping previous set")
		return
	}
	ids := make(map[string]struct{}, len(m))
	for id := range m {
		ids[id] = struct{}{}
	}
	coreauth.SetReservedCredentials(ids)
	r.reserve.since.Store(m)
	if r.ImportCheckSettings().BlockFailed {
		if hs, ok := s.(interface {
			ListHealthFailedIDs(context.Context) (map[string]struct{}, error)
		}); ok {
			if failed, err := hs.ListHealthFailedIDs(cctx); err == nil {
				coreauth.SetHealthBlockedCredentials(failed)
			}
		}
	} else {
		coreauth.SetHealthBlockedCredentials(nil)
	}
}

// ReserveChanged re-reads the reserve set now and tells the other Homes to do the same.
func (r *Runtime) ReserveChanged(ctx context.Context) {
	r.syncReserve(ctx)
	publishQuotaSync(quotaSyncMsg{Kind: "reserve"})
}

// SetCredentialsReserved is the management entry (bulk action / import toggle).
func (r *Runtime) SetCredentialsReserved(ctx context.Context, ids []string, reserved bool, detail string) (int, error) {
	s := r.reserveStore()
	if s == nil {
		return 0, fmt.Errorf("credential reserve needs the cluster store")
	}
	kind := "reserve"
	if !reserved {
		kind = "unreserve"
	}
	n, err := s.SetCredentialsReserved(ctx, ids, reserved, kind, detail)
	if n > 0 {
		r.ReserveChanged(ctx)
	}
	return n, err
}

// ReserveStatus fills controller-side fields of the panel stats.
// Active/eligible are computed live from the store, so any Home (not only the leader
// running the promoter) reports them correctly.
func (r *Runtime) ReserveStatus(ctx context.Context, st *ReserveStats) {
	cfg := r.reserveSettings()
	st.MinActive = cfg.MinActive
	st.Active, st.Eligible, st.Enabled, st.Usable = -1, -1, -1, -1
	st.CountCooled = cfg.CountCooled
	if s := r.reserveStore(); s != nil {
		if n, err := s.CountActiveCredentials(ctx, true); err == nil {
			st.Enabled = n
		}
		if n, err := s.CountActiveCredentials(ctx, false); err == nil {
			st.Usable = n
		}
		if cfg.CountCooled {
			st.Active = st.Enabled
		} else {
			st.Active = st.Usable
		}
		if c, err := s.ListReserveCandidates(ctx); err == nil {
			ordered := orderReserveCandidates(c, cfg.Tiers, r.ImportCheckSettings().Promote)
			st.Eligible = len(ordered)
			for _, ev := range evalModelRules(ctx, s, cfg.ModelRules, ordered) {
				st.ModelRules = append(st.ModelRules, ReserveModelStat{Pattern: ev.rule.Pattern, Min: ev.rule.Min,
					Usable: ev.usable, Reserves: len(ev.cands), Short: ev.usable < ev.rule.Min && len(ev.cands) == 0})
			}
		}
	}
	st.Short = st.MinActive > 0 && st.Active >= 0 && st.Active < st.MinActive && st.Eligible == 0
	for _, m := range st.ModelRules {
		st.Short = st.Short || m.Short
	}
	r.reserve.mu.Lock()
	if !r.reserve.shortfallSince.IsZero() {
		st.ShortfallSinceUnix = r.reserve.shortfallSince.Unix()
	}
	for i := range st.ModelRules {
		if t, ok := r.reserve.modelSince[st.ModelRules[i].Pattern]; ok {
			st.ModelRules[i].Since = t.Unix()
		}
	}
	r.reserve.mu.Unlock()
}

func (r *Runtime) reserveSettings() config.ReserveSettings {
	if r.reserveCfgOverride != nil {
		return r.reserveCfgOverride.ReserveSettings()
	}
	return r.Config().ReserveSettings()
}

func (r *Runtime) startCredentialReserve(ctx context.Context) {
	r.syncReserve(ctx) // before serving: never schedule a reserved credential at startup
	r.startReserveBanProbe(ctx)
	go func() {
		sync := time.NewTicker(reserveSyncEvery)
		ctl := time.NewTicker(reserveControlEvery)
		defer sync.Stop()
		defer ctl.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sync.C:
				r.syncReserve(ctx)
			case <-ctl.C:
				if r.catalogLeader() {
					r.reserveControl(ctx, time.Now())
					r.healthSweep(ctx)
					r.regionScan(ctx)
				}
			}
		}
	}()
}

// logReserveState logs only when the shortfall state changes (caller holds reserve.mu).
func (r *Runtime) logReserveState(state string, logf func()) {
	if r.reserve.state == state {
		return
	}
	r.reserve.state = state
	logf()
}

// healthAllowsPromotion applies the import health check promotion policy:
// pass = only passed; not-failed = passed or unchecked; any = ignore checks.
func healthAllowsPromotion(health, policy string) bool {
	switch policy {
	case "any":
		return true
	case "not-failed":
		return health != "fail"
	default:
		return health == "pass"
	}
}

func reserveTierClass(tier string) string {
	t := strings.ToLower(tier)
	switch {
	case strings.Contains(t, "ultra"):
		return "ultra"
	case strings.Contains(t, "pro"):
		return "pro"
	case t != "":
		return "free"
	}
	return "unknown"
}

// orderReserveCandidates applies the tier filter and the promotion order: healthy
// first, then oldest import (sequence number, then created_at).
func orderReserveCandidates(in []ReserveCandidate, tiers []string, healthPolicy string) []ReserveCandidate {
	allow := map[string]bool{}
	for _, t := range tiers {
		allow[t] = true
	}
	out := make([]ReserveCandidate, 0, len(in))
	for _, c := range in {
		if len(allow) > 0 && !allow[reserveTierClass(c.Tier)] {
			continue
		}
		if !healthAllowsPromotion(c.Health, healthPolicy) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Healthy != b.Healthy {
			return a.Healthy
		}
		if a.Seq != b.Seq && a.Seq > 0 && b.Seq > 0 {
			return a.Seq < b.Seq
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out
}

// reserveControl runs on the leader: audits bans, then promotes reserves when the
// active count (or a model rule's usable count) has stayed below its minimum for the
// activation delay, rate-limited. Model-rule shortages promote rule-capable reserves
// (most remaining quota first); a generic shortage prefers them while a rule is short.
func (r *Runtime) reserveControl(ctx context.Context, now time.Time) {
	s := r.reserveStore()
	if s == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if n, err := s.RecordReserveBans(cctx); err != nil {
		log.WithError(err).Debug("credential reserve: ban audit failed")
	} else if n > 0 {
		log.Infof("credential reserve: recorded %d ban(s)", n)
	}
	cfg := r.reserveSettings()
	active, err := s.CountActiveCredentials(cctx, cfg.CountCooled)
	if err != nil {
		log.WithError(err).Debug("credential reserve: active count failed")
		return
	}
	r.reserve.lastActive.Store(int64(active))
	r.reserve.mu.Lock()
	defer r.reserve.mu.Unlock()
	genericShort := cfg.MinActive > 0 && active < cfg.MinActive
	if !genericShort {
		if !r.reserve.shortfallSince.IsZero() {
			log.Infof("credential reserve: active %d >= minimum %d, shortfall cleared", active, cfg.MinActive)
		}
		r.reserve.shortfallSince = time.Time{}
	} else if r.reserve.shortfallSince.IsZero() {
		r.reserve.shortfallSince = now
	}
	var cands []ReserveCandidate
	if genericShort || len(cfg.ModelRules) > 0 {
		all, err := s.ListReserveCandidates(cctx)
		if err != nil {
			log.WithError(err).Debug("credential reserve: candidate lookup failed")
			return
		}
		cands = orderReserveCandidates(all, cfg.Tiers, r.ImportCheckSettings().Promote)
	}
	evals := evalModelRules(cctx, s, cfg.ModelRules, cands)
	// Rule shortages: sustained ones get their own promotions.
	var shortRules []string
	var priority []ReserveCandidate // rule-capable reserves of short rules, rule order
	seen := map[string]bool{}
	nRule := 0
	for _, ev := range evals {
		short := ev.usable < ev.rule.Min
		since := r.modelShortSince(ev.rule.Pattern, short, now)
		if !short {
			continue
		}
		shortRules = append(shortRules, fmt.Sprintf("%s 可用 %d < %d", ev.rule.Pattern, ev.usable, ev.rule.Min))
		need := 0
		if now.Sub(since) >= cfg.ActivationDelay {
			need = ev.rule.Min - ev.usable
		}
		for _, c := range ev.cands {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			priority = append(priority, c)
			if need > 0 {
				need--
				nRule++
			}
		}
	}
	if !genericShort && len(shortRules) == 0 {
		r.reserve.eligible.Store(-1)
		r.reserve.state = "ok"
		return
	}
	r.reserve.eligible.Store(int64(len(cands)))
	if len(cands) == 0 {
		r.logReserveState("empty", func() {
			log.Warnf("credential reserve: 储备不足 — active %d (excl. Free tier) < minimum %d / %v and no eligible reserve credential (tier filter %v)", active, cfg.MinActive, shortRules, cfg.Tiers)
		})
		return
	}
	nGeneric := 0
	if genericShort && now.Sub(r.reserve.shortfallSince) >= cfg.ActivationDelay {
		nGeneric = cfg.MinActive - active - nRule
		if nGeneric < 0 {
			nGeneric = 0
		}
	}
	n := nRule + nGeneric
	if n <= 0 {
		r.logReserveState("waiting", func() {
			log.Infof("credential reserve: active %d / minimum %d, model rules short %v; promoting after %s if it persists (%d eligible)", active, cfg.MinActive, shortRules, cfg.ActivationDelay, len(cands))
		})
		return
	}
	list := priority
	if nGeneric > 0 {
		list = append(append([]ReserveCandidate(nil), priority...), excludeCandidates(cands, seen)...)
	}
	if n > len(list) {
		n = len(list)
	}
	if n <= 0 {
		return
	}
	used, err := s.CountReserveEvents(cctx, "promote", now.Add(-cfg.PromotionWindow))
	if err != nil {
		return
	}
	if budget := cfg.MaxPromotions - used; n > budget {
		n = budget
	}
	if n <= 0 {
		r.logReserveState("limited", func() {
			log.Warnf("credential reserve: active %d / minimum %d, model rules short %v, but promotion limit %d per %s reached", active, cfg.MinActive, shortRules, cfg.MaxPromotions, cfg.PromotionWindow)
		})
		return
	}
	picked := list[:n]
	if cfg.PrePromoteCheck {
		var banned int
		picked, banned = r.prePromoteFilter(ctx, list, n, now.UTC())
		if banned > 0 {
			log.Warnf("credential reserve: pre-promotion probe found %d banned reserve(s); skipped", banned)
			r.syncReserve(ctx)
		}
		if len(picked) == 0 {
			r.reserve.state = "probing"
			return
		}
		n = len(picked)
	}
	r.reserve.state = "promoted"
	ids := make([]string, 0, n)
	for _, c := range picked {
		ids = append(ids, c.ID)
	}
	var why []string
	if genericShort {
		why = append(why, fmt.Sprintf("可用 %d < 最低 %d（持续 %s）", active, cfg.MinActive, now.Sub(r.reserve.shortfallSince).Round(time.Second)))
	}
	why = append(why, shortRules...)
	detail := "自动启用：" + strings.Join(why, "；")
	sctx, scancel := context.WithTimeout(ctx, 15*time.Second) // probes may have used cctx's budget
	defer scancel()
	done, err := s.SetCredentialsReserved(sctx, ids, false, "promote", detail)
	if err != nil {
		log.WithError(err).Warn("credential reserve: promotion failed")
		return
	}
	for _, c := range picked {
		log.Infof("credential reserve: promoted %s (seq %d, tier %s, healthy %v, model-rule %v): %s", c.ID, c.Seq, reserveTierClass(c.Tier), c.Healthy, seen[c.ID], detail)
	}
	if done > 0 {
		r.syncReserve(ctx)
		publishQuotaSync(quotaSyncMsg{Kind: "reserve"})
	}
}

func excludeCandidates(in []ReserveCandidate, skip map[string]bool) []ReserveCandidate {
	out := make([]ReserveCandidate, 0, len(in))
	for _, c := range in {
		if !skip[c.ID] {
			out = append(out, c)
		}
	}
	return out
}
