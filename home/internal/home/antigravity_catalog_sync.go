package home

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Antigravity catalog synchronisation.
//
// Every credential's upstream model catalog (fetchAvailableModels) decides
// which models the credential is registered for, i.e. what /v1/models lists
// and where requests are routed. To keep all Home replicas consistent and the
// upstream call count low:
//   - only the Home leader fetches on the timer (routing.antigravity-catalog-refresh,
//     default 15m) and stores the raw catalog in the database;
//   - every replica polls the stored rows and applies newer ones;
//   - a 404 / 400 for a model on a credential triggers an immediate,
//     debounced (<= 1/min per credential) refresh from whichever replica saw it;
//   - the panel can force a refresh.
// Models that disappear are remembered for a week as "removed upstream".

const (
	defaultCatalogRefreshInterval = 15 * time.Minute
	minCatalogRefreshInterval     = time.Minute
	catalogTickInterval           = 30 * time.Second
	catalogErrorBackoff           = 5 * time.Minute
	catalogEventDebounce          = time.Minute
	catalogChangeRetention        = 7 * 24 * time.Hour
)

// AntigravityCatalogRow is the persisted catalog state of one credential.
type AntigravityCatalogRow struct {
	AuthID      string
	Raw         []byte
	FetchedAt   time.Time // last successful fetch
	AttemptedAt time.Time // last attempt (success or error)
	Status      string    // ok | error
	Error       string
	HTTPStatus  int
	ModelCount  int
	Added       string // JSON {"model": "RFC3339"} models that appeared within the retention window
	Removed     string // JSON {"model": "RFC3339"} models that disappeared within the retention window
	Trigger     string // timer | startup | manual | event:<reason>
	FetchedBy   string
}

// AntigravityCatalogStore persists catalog rows (implemented by the cluster adapter).
type AntigravityCatalogStore interface {
	LoadAntigravityCatalogs(ctx context.Context) (map[string]AntigravityCatalogRow, error)
	SaveAntigravityCatalog(ctx context.Context, row AntigravityCatalogRow) error
}

// AntigravityCatalogIncrementalStore is optionally implemented by stores that
// can return only rows attempted/fetched after a time (avoids re-reading every
// catalog blob each tick at thousands of credentials).
type AntigravityCatalogIncrementalStore interface {
	LoadAntigravityCatalogsSince(ctx context.Context, since time.Time) (map[string]AntigravityCatalogRow, error)
}

// catalogFullReloadEvery forces a full reload periodically (safety net).
const catalogFullReloadEvery = 10 * time.Minute

// catalogFetchConcurrency bounds parallel leader fetches; env
// HOME_CATALOG_FETCH_CONCURRENCY overrides (default 4).
func catalogFetchConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HOME_CATALOG_FETCH_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return 4
}

type antigravityCatalogSync struct {
	loadedAt   time.Time
	fullLoadAt time.Time
	leaderFn   atomic.Value // func() bool
	mu         sync.Mutex
	rows       map[string]AntigravityCatalogRow // latest known row per credential
	inFlight   map[string]bool
	lastEvent  map[string]time.Time
	started    atomic.Bool
	kick       atomic.Bool
}

// SetLeaderFunc tells the runtime how to know whether this replica is the
// cluster leader (only the leader fetches on the timer).
func (r *Runtime) SetLeaderFunc(fn func() bool) {
	if r == nil || fn == nil {
		return
	}
	r.agCatalog.leaderFn.Store(fn)
}

func (r *Runtime) catalogLeader() bool {
	if fn, ok := r.agCatalog.leaderFn.Load().(func() bool); ok && fn != nil {
		return fn()
	}
	return true // single node / no coordinator
}

func (r *Runtime) catalogStore() AntigravityCatalogStore {
	if r == nil || r.clusterAdapter == nil || !r.clusterAdapter.Enabled() {
		return nil
	}
	s, _ := r.clusterAdapter.(AntigravityCatalogStore)
	return s
}

// AntigravityCatalogInterval returns the configured timer interval (0 = timer off).
func (r *Runtime) AntigravityCatalogInterval() time.Duration {
	var raw string
	if cfg := r.Config(); cfg != nil {
		raw = cfg.AntigravityCatalogRefresh
	}
	return parseCatalogInterval(raw)
}

func parseCatalogInterval(raw string) time.Duration {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "":
		return defaultCatalogRefreshInterval
	case "0", "off", "false", "disabled":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warnf("invalid routing.antigravity-catalog-refresh %q, using %s", raw, defaultCatalogRefreshInterval)
		return defaultCatalogRefreshInterval
	}
	if d == 0 {
		return 0
	}
	if d < minCatalogRefreshInterval {
		return minCatalogRefreshInterval
	}
	return d
}

func (s *antigravityCatalogSync) init() {
	if s.rows == nil {
		s.rows = map[string]AntigravityCatalogRow{}
		s.inFlight = map[string]bool{}
		s.lastEvent = map[string]time.Time{}
	}
}

func (r *Runtime) catalogRow(authID string) (AntigravityCatalogRow, bool) {
	r.agCatalog.mu.Lock()
	defer r.agCatalog.mu.Unlock()
	r.agCatalog.init()
	row, ok := r.agCatalog.rows[authID]
	return row, ok
}

func (r *Runtime) antigravityAuths() []*coreauth.Auth {
	var out []*coreauth.Auth
	if r == nil || r.coreManager == nil {
		return out
	}
	out = r.coreManager.ListLight(func(a *coreauth.Auth) bool {
		return !a.Disabled && strings.EqualFold(a.Provider, "antigravity")
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// startAntigravityCatalogLoop runs the shared catalog timer/poller.
func (r *Runtime) startAntigravityCatalogLoop(ctx context.Context) {
	if r == nil || !antigravityDynamicModelsEnabled() || !r.agCatalog.started.CompareAndSwap(false, true) {
		return
	}
	if r.coreManager != nil {
		r.coreManager.SetModelErrorHook(func(authID, provider, model string, status int, msg string) {
			if !strings.EqualFold(provider, "antigravity") {
				a, ok := r.coreManager.GetByID(authID)
				if !ok || a == nil || !strings.EqualFold(a.Provider, "antigravity") {
					return
				}
			}
			if status != 429 {
				r.TriggerAntigravityCatalogRefresh(authID, strconv.Itoa(status)+" "+model)
			}
			if subscriptionQuotaMessage(status, msg) {
				r.RequestSubscriptionCheck(authID, "429 subscription quota")
			}
		})
	}
	go func() {
		r.antigravityCatalogTick(ctx, "startup")
		t := time.NewTicker(catalogTickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.antigravityCatalogTick(ctx, "timer")
			}
		}
	}()
}

// antigravityCatalogTick applies newer stored rows and, on the leader, fetches
// catalogs that are due.
func (r *Runtime) antigravityCatalogTick(ctx context.Context, trigger string) {
	auths := r.antigravityAuths()
	if len(auths) == 0 {
		return
	}
	r.applyStoredAntigravityCatalogs(ctx, auths)
	r.pruneCatalogState(auths)
	if !r.catalogLeader() {
		return
	}
	interval := r.AntigravityCatalogInterval()
	now := time.Now()
	sem := make(chan struct{}, catalogFetchConcurrency())
	var wg sync.WaitGroup
	for _, a := range auths {
		row, ok := r.catalogRow(a.ID)
		if !catalogDue(row, ok, interval, now) {
			continue
		}
		id := a.ID
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if _, err := r.refreshAntigravityCatalogNow(ctx, id, trigger); err != nil && !errors.Is(err, errCatalogInFlight) {
				log.Warnf("antigravity catalog refresh (%s) for %s: %v", trigger, id, err)
			}
		}()
	}
	wg.Wait()
}

// applyStoredAntigravityCatalogs loads stored catalog rows and applies them to auths.
func (r *Runtime) applyStoredAntigravityCatalogs(ctx context.Context, auths []*coreauth.Auth) {
	store := r.catalogStore()
	if store == nil {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	start := time.Now()
	r.agCatalog.mu.Lock()
	since, fullAt := r.agCatalog.loadedAt, r.agCatalog.fullLoadAt
	r.agCatalog.mu.Unlock()
	var rows map[string]AntigravityCatalogRow
	var err error
	inc, incremental := store.(AntigravityCatalogIncrementalStore)
	if incremental && !since.IsZero() && start.Sub(fullAt) < catalogFullReloadEvery {
		// small overlap guards against clock skew between Homes
		rows, err = inc.LoadAntigravityCatalogsSince(loadCtx, since.Add(-time.Minute))
	} else {
		rows, err = store.LoadAntigravityCatalogs(loadCtx)
		if err == nil {
			r.agCatalog.mu.Lock()
			r.agCatalog.fullLoadAt = start
			r.agCatalog.mu.Unlock()
		}
	}
	cancel()
	if err == nil {
		r.agCatalog.mu.Lock()
		r.agCatalog.loadedAt = start
		r.agCatalog.mu.Unlock()
	}
	if err != nil {
		log.Warnf("antigravity catalog: load stored rows: %v", err)
	}
	for _, a := range auths {
		if row, ok := rows[a.ID]; ok {
			r.applyCatalogRow(a.ID, row)
		}
	}
}

// WarmAntigravityCatalogs applies stored dynamic catalogs synchronously. Called at startup
// after auths load and before the Home joins the cluster, so the first dispatches see every
// credential's full model list (otherwise only credentials whose catalog happened to load
// first are eligible and the rest look "all cooling" for up to one tick).
func (r *Runtime) WarmAntigravityCatalogs(ctx context.Context) {
	if r == nil || !antigravityDynamicModelsEnabled() {
		return
	}
	if auths := r.antigravityAuths(); len(auths) > 0 {
		r.applyStoredAntigravityCatalogs(ctx, auths)
	}
}

// pruneCatalogState drops cached rows/event marks of deleted or disabled credentials.
func (r *Runtime) pruneCatalogState(auths []*coreauth.Auth) {
	live := make(map[string]struct{}, len(auths))
	for _, a := range auths {
		live[a.ID] = struct{}{}
	}
	r.agCatalog.mu.Lock()
	defer r.agCatalog.mu.Unlock()
	for id := range r.agCatalog.rows {
		if _, ok := live[id]; !ok && !r.agCatalog.inFlight[id] {
			delete(r.agCatalog.rows, id)
		}
	}
	for id := range r.agCatalog.lastEvent {
		if _, ok := live[id]; !ok {
			delete(r.agCatalog.lastEvent, id)
		}
	}
	// Forget the in-memory catalog of removed/disabled credentials so a later
	// re-enable fetches fresh instead of reusing a frozen model list.
	antigravityCatalogMu.Lock()
	for id := range antigravityCatalog {
		if _, ok := live[id]; !ok && !r.agCatalog.inFlight[id] {
			delete(antigravityCatalog, id)
		}
	}
	antigravityCatalogMu.Unlock()
}

// catalogDue decides whether the leader should fetch a credential's catalog.
func catalogDue(row AntigravityCatalogRow, known bool, interval time.Duration, now time.Time) bool {
	if known && row.Status == "error" && now.Sub(row.AttemptedAt) < catalogErrorBackoff {
		return false
	}
	if !known || row.FetchedAt.IsZero() {
		return true
	}
	return interval > 0 && now.Sub(row.FetchedAt) >= interval
}

// TriggerAntigravityCatalogRefresh asks for an immediate refresh of one
// credential after a model-level 404/400; debounced to once a minute.
func (r *Runtime) TriggerAntigravityCatalogRefresh(authID, reason string) bool {
	if r == nil || !antigravityDynamicModelsEnabled() || strings.TrimSpace(authID) == "" {
		return false
	}
	now := time.Now()
	r.agCatalog.mu.Lock()
	r.agCatalog.init()
	last := r.agCatalog.lastEvent[authID]
	if row, ok := r.agCatalog.rows[authID]; ok && row.AttemptedAt.After(last) {
		last = row.AttemptedAt
	}
	if now.Sub(last) < catalogEventDebounce {
		r.agCatalog.mu.Unlock()
		return false
	}
	r.agCatalog.lastEvent[authID] = now
	r.agCatalog.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// Another replica may have refreshed already; check the shared row first.
		if store := r.catalogStore(); store != nil {
			if rows, err := store.LoadAntigravityCatalogs(ctx); err == nil {
				if row, ok := rows[authID]; ok {
					r.applyCatalogRow(authID, row)
					if time.Since(row.AttemptedAt) < catalogEventDebounce {
						return
					}
				}
			}
		}
		if _, err := r.refreshAntigravityCatalogNow(ctx, authID, "event:"+reason); err != nil {
			log.Warnf("antigravity catalog event refresh for %s: %v", authID, err)
		}
	}()
	return true
}

// RefreshAntigravityCatalogs fetches now (manual). authID "" = all credentials.
func (r *Runtime) RefreshAntigravityCatalogs(ctx context.Context, authID string) []AntigravityCatalogStatus {
	for _, a := range r.antigravityAuths() {
		if authID != "" && a.ID != authID {
			continue
		}
		if _, err := r.refreshAntigravityCatalogNow(ctx, a.ID, "manual"); err != nil {
			log.Warnf("antigravity catalog manual refresh for %s: %v", a.ID, err)
		}
	}
	return r.AntigravityCatalogStatuses()
}

var errCatalogInFlight = errors.New("refresh already running")

// refreshAntigravityCatalogNow fetches one credential's catalog, stores the
// row (with added/removed diff) and applies it locally.
func (r *Runtime) refreshAntigravityCatalogNow(ctx context.Context, authID, trigger string) (AntigravityCatalogRow, error) {
	r.agCatalog.mu.Lock()
	r.agCatalog.init()
	if r.agCatalog.inFlight[authID] {
		r.agCatalog.mu.Unlock()
		return AntigravityCatalogRow{}, errCatalogInFlight
	}
	r.agCatalog.inFlight[authID] = true
	prev, hasPrev := r.agCatalog.rows[authID]
	r.agCatalog.mu.Unlock()
	defer func() {
		r.agCatalog.mu.Lock()
		delete(r.agCatalog.inFlight, authID)
		r.agCatalog.mu.Unlock()
	}()

	var auth *coreauth.Auth
	if r.coreManager != nil {
		auth, _ = r.coreManager.GetByID(authID)
	}
	if auth == nil {
		return AntigravityCatalogRow{}, antigravityStringError("credential not found")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	full := auth
	var err error
	if extractAccessToken(full) == "" && r.clusterAdapter != nil && r.clusterAdapter.Enabled() {
		var loaded *coreauth.Auth
		if loaded, err = r.clusterAdapter.GetFullAuth(fetchCtx, authID); err == nil && loaded != nil {
			full = loaded
		}
	}
	var raw []byte
	if err == nil {
		raw, err = fetchAntigravityCatalogRaw(fetchCtx, full)
	}
	var models map[string]antigravityUpstreamModel
	if err == nil {
		models, err = parseAntigravityCatalog(raw)
	}
	now := time.Now().UTC()
	host, _ := os.Hostname()
	row := prev
	if !hasPrev {
		row = AntigravityCatalogRow{AuthID: authID}
	}
	row.AttemptedAt, row.Trigger, row.FetchedBy = now, trigger, host
	if err != nil {
		row.Status, row.Error, row.HTTPStatus = "error", err.Error(), 0
		var he *antigravityHTTPError
		if errors.As(err, &he) {
			row.HTTPStatus = he.status
			// A TOS / suspended 403 on the model list is a ban (same as a generate 403).
			r.disableForListBan(ctx, authID, "fetchAvailableModels", he.status, he.body, now)
		}
	} else {
		var prevModels map[string]antigravityUpstreamModel
		if len(prev.Raw) > 0 {
			prevModels, _ = parseAntigravityCatalog(prev.Raw)
		}
		row.Added, row.Removed = diffCatalog(prevModels, models, prev.Added, prev.Removed, now)
		if hasPrev && len(prevModels) > 0 && !sameModelSet(prevModels, models) {
			r.RequestSubscriptionCheck(authID, "catalog changed")
		}
		row.Raw, row.FetchedAt, row.Status, row.Error, row.HTTPStatus, row.ModelCount = raw, now, "ok", "", 200, len(models)
	}
	if store := r.catalogStore(); store != nil {
		saveCtx, cancelSave := context.WithTimeout(context.Background(), 10*time.Second)
		if errSave := store.SaveAntigravityCatalog(saveCtx, row); errSave != nil {
			log.Warnf("antigravity catalog: store row for %s: %v", authID, errSave)
		}
		cancelSave()
	}
	r.applyCatalogRow(authID, row)
	return row, err
}

// diffCatalog maintains the added/removed maps (model -> first seen time).
func diffCatalog(prev, next map[string]antigravityUpstreamModel, prevAdded, prevRemoved string, now time.Time) (string, string) {
	added := decodeChangeMap(prevAdded)
	removed := decodeChangeMap(prevRemoved)
	if prev != nil {
		for id, m := range next {
			if _, ok := prev[id]; !ok {
				added[m.ID] = now.Format(time.RFC3339)
			}
		}
		for id, m := range prev {
			if _, ok := next[id]; !ok {
				removed[m.ID] = now.Format(time.RFC3339)
			}
		}
	}
	for id := range removed { // came back
		if _, ok := next[strings.ToLower(id)]; ok {
			delete(removed, id)
		}
	}
	for id := range added {
		if _, ok := next[strings.ToLower(id)]; !ok {
			delete(added, id)
		}
	}
	prune := func(m map[string]string) {
		for id, at := range m {
			if t, err := time.Parse(time.RFC3339, at); err != nil || now.Sub(t) > catalogChangeRetention {
				delete(m, id)
			}
		}
	}
	prune(added)
	prune(removed)
	return encodeChangeMap(added), encodeChangeMap(removed)
}

func decodeChangeMap(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func encodeChangeMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// applyCatalogRow records the row and, when it carries a newer successful
// catalog, updates the in-memory catalog and re-registers the credential.
func (r *Runtime) applyCatalogRow(authID string, row AntigravityCatalogRow) {
	r.agCatalog.mu.Lock()
	r.agCatalog.init()
	cur, ok := r.agCatalog.rows[authID]
	if ok && row.AttemptedAt.Before(cur.AttemptedAt) && !row.FetchedAt.After(cur.FetchedAt) {
		r.agCatalog.mu.Unlock()
		return
	}
	r.agCatalog.rows[authID] = row
	r.agCatalog.mu.Unlock()
	if len(row.Raw) == 0 || row.FetchedAt.IsZero() {
		return
	}
	antigravityCatalogMu.Lock()
	entry := antigravityCatalog[authID]
	if entry == nil {
		entry = &antigravityCatalogEntry{}
		antigravityCatalog[authID] = entry
	}
	if entry.models != nil && !row.FetchedAt.After(entry.fetchedAt) {
		antigravityCatalogMu.Unlock()
		return
	}
	antigravityCatalogMu.Unlock()
	models, err := parseAntigravityCatalog(row.Raw)
	if err != nil {
		log.Warnf("antigravity catalog: stored row for %s is invalid: %v", authID, err)
		return
	}
	health := parseAntigravityCatalogHealth(row.Raw)
	health.fetchedAt = row.FetchedAt
	antigravityCatalogMu.Lock()
	changed := !sameAntigravityCatalog(entry.models, models)
	entry.models, entry.health, entry.fetchedAt = models, health, row.FetchedAt
	antigravityCatalogMu.Unlock()
	r.applyCatalogQuotaGroups(authID, health)
	if changed && r.coreManager != nil {
		if auth, ok := r.coreManager.GetByID(authID); ok && auth != nil {
			log.Infof("antigravity model catalog for %s: %d upstream models (%s)", authID, len(models), row.Trigger)
			r.registerModelsForAuth(auth)
			r.coreManager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			r.coreManager.RefreshSchedulerEntry(auth.ID)
		}
	}
}

// CatalogChange is one added/removed model.
type CatalogChange struct {
	Model string `json:"model"`
	At    string `json:"at"`
}

// AntigravityCatalogStatus is the panel view of one credential's catalog.
type AntigravityCatalogStatus struct {
	AuthID      string          `json:"auth_id"`
	Label       string          `json:"label"`
	Status      string          `json:"status"` // ok | error | pending
	Error       string          `json:"error,omitempty"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	ModelCount  int             `json:"model_count"`
	FetchedAt   *time.Time      `json:"fetched_at,omitempty"`
	AttemptedAt *time.Time      `json:"attempted_at,omitempty"`
	NextDueAt   *time.Time      `json:"next_due_at,omitempty"`
	Trigger     string          `json:"trigger,omitempty"`
	FetchedBy   string          `json:"fetched_by,omitempty"`
	Added       []CatalogChange `json:"added"`
	Removed     []CatalogChange `json:"removed"`
}

func changeList(raw string) []CatalogChange {
	m := decodeChangeMap(raw)
	out := make([]CatalogChange, 0, len(m))
	for id, at := range m {
		out = append(out, CatalogChange{Model: id, At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// AntigravityCatalogStatuses lists the catalog state of every enabled
// Antigravity credential.
func (r *Runtime) AntigravityCatalogStatuses() []AntigravityCatalogStatus {
	interval := r.AntigravityCatalogInterval()
	var out []AntigravityCatalogStatus
	for _, a := range r.antigravityAuths() {
		st := AntigravityCatalogStatus{AuthID: a.ID, Label: authDisplayLabel(a), Status: "pending", Added: []CatalogChange{}, Removed: []CatalogChange{}}
		if row, ok := r.catalogRow(a.ID); ok {
			st.Status, st.Error, st.HTTPStatus, st.ModelCount, st.Trigger, st.FetchedBy = row.Status, row.Error, row.HTTPStatus, row.ModelCount, row.Trigger, row.FetchedBy
			if !row.FetchedAt.IsZero() {
				t := row.FetchedAt
				st.FetchedAt = &t
				if interval > 0 {
					n := t.Add(interval)
					st.NextDueAt = &n
				}
			}
			if !row.AttemptedAt.IsZero() {
				t := row.AttemptedAt
				st.AttemptedAt = &t
			}
			st.Added, st.Removed = changeList(row.Added), changeList(row.Removed)
		}
		out = append(out, st)
	}
	return out
}

// CatalogLeader reports whether this replica runs the catalog timer.
func (r *Runtime) CatalogLeader() bool { return r.catalogLeader() }

// sameModelSet reports whether two catalogs list the same model ids.
func sameModelSet(a, b map[string]antigravityUpstreamModel) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// subscriptionQuotaMessage spots quota errors that hint at the plan
// ("Individual quota reached. Please upgrade your subscription ...").
func subscriptionQuotaMessage(status int, msg string) bool {
	if status != 429 {
		return false
	}
	l := strings.ToLower(msg)
	return strings.Contains(l, "subscription") || strings.Contains(l, "individual quota")
}
