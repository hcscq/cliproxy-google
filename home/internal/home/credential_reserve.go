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
// health check. Quota-cooled credentials count by default (routing.reserve.count-cooled)
// since cooldowns are temporary and would cause promotion churn.

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
	Reserved           int   `json:"reserved"`
	EverReserved       int   `json:"ever_reserved"`
	BansReserved       int   `json:"bans_reserved"`        // banned while in reserve
	BansActive         int   `json:"bans_active"`          // banned while schedulable
	BansAfterPromotion int   `json:"bans_after_promotion"` // subset of bans_active: had been reserved before
	Promotions24h      int   `json:"promotions_24h"`
	MinActive          int   `json:"min_active"`
	Active             int   `json:"active"`
	Eligible           int   `json:"eligible"` // reserves matching the tier filter (-1 = not checked)
	Short              bool  `json:"short"`    // active < min and no eligible reserve
	ShortfallSinceUnix int64 `json:"shortfall_since,omitempty"`
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
	state          string       // last logged shortfall state (ok|waiting|limited|empty|promoted)
	eligible       atomic.Int64 // eligible reserves at the last check (-1 = unknown)
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
	st.Active, st.Eligible = -1, -1
	if s := r.reserveStore(); s != nil {
		if n, err := s.CountActiveCredentials(ctx, cfg.CountCooled); err == nil {
			st.Active = n
		}
		if c, err := s.ListReserveCandidates(ctx); err == nil {
			st.Eligible = len(orderReserveCandidates(c, cfg.Tiers, r.ImportCheckSettings().Promote))
		}
	}
	st.Short = st.MinActive > 0 && st.Active >= 0 && st.Active < st.MinActive && st.Eligible == 0
	r.reserve.mu.Lock()
	if !r.reserve.shortfallSince.IsZero() {
		st.ShortfallSinceUnix = r.reserve.shortfallSince.Unix()
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
// active count has stayed below the minimum for the activation delay, rate-limited.
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
	if cfg.MinActive <= 0 || active >= cfg.MinActive {
		r.reserve.eligible.Store(-1)
		if !r.reserve.shortfallSince.IsZero() {
			log.Infof("credential reserve: active %d >= minimum %d, shortfall cleared", active, cfg.MinActive)
		}
		r.reserve.shortfallSince = time.Time{}
		r.reserve.state = "ok"
		return
	}
	// Short: count eligible reserves every check so the panel can warn even before
	// the activation delay; with none, nothing below runs (no promotion, no errors).
	cands, err := s.ListReserveCandidates(cctx)
	if err != nil {
		log.WithError(err).Debug("credential reserve: candidate lookup failed")
		return
	}
	cands = orderReserveCandidates(cands, cfg.Tiers, r.ImportCheckSettings().Promote)
	r.reserve.eligible.Store(int64(len(cands)))
	if r.reserve.shortfallSince.IsZero() {
		r.reserve.shortfallSince = now
	}
	if len(cands) == 0 {
		r.logReserveState("empty", func() {
			log.Warnf("credential reserve: 储备不足 — active %d < minimum %d and no eligible reserve credential (tier filter %v)", active, cfg.MinActive, cfg.Tiers)
		})
		return
	}
	if now.Sub(r.reserve.shortfallSince) < cfg.ActivationDelay {
		r.logReserveState("waiting", func() {
			log.Infof("credential reserve: active %d < minimum %d; promoting after %s if it persists (%d eligible)", active, cfg.MinActive, cfg.ActivationDelay, len(cands))
		})
		return
	}
	used, err := s.CountReserveEvents(cctx, "promote", now.Add(-cfg.PromotionWindow))
	if err != nil {
		return
	}
	n := cfg.MinActive - active
	if budget := cfg.MaxPromotions - used; n > budget {
		n = budget
	}
	if n <= 0 {
		r.logReserveState("limited", func() {
			log.Warnf("credential reserve: active %d < minimum %d but promotion limit %d per %s reached", active, cfg.MinActive, cfg.MaxPromotions, cfg.PromotionWindow)
		})
		return
	}
	r.reserve.state = "promoted"
	if n > len(cands) {
		n = len(cands)
	}
	ids := make([]string, 0, n)
	for _, c := range cands[:n] {
		ids = append(ids, c.ID)
	}
	detail := fmt.Sprintf("自动启用：可用 %d < 最低 %d（持续 %s）", active, cfg.MinActive, now.Sub(r.reserve.shortfallSince).Round(time.Second))
	done, err := s.SetCredentialsReserved(cctx, ids, false, "promote", detail)
	if err != nil {
		log.WithError(err).Warn("credential reserve: promotion failed")
		return
	}
	for _, c := range cands[:n] {
		log.Infof("credential reserve: promoted %s (seq %d, tier %s, healthy %v): %s", c.ID, c.Seq, reserveTierClass(c.Tier), c.Healthy, detail)
	}
	if done > 0 {
		r.syncReserve(ctx)
		publishQuotaSync(quotaSyncMsg{Kind: "reserve"})
	}
}
