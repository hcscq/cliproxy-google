package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Scope cache: per-replica in-memory snapshot of API keys, credential groups
// (static members + rule matches), model groups and their details, used on
// every dispatch instead of 3-5 queries per request.
//
// Stateless invalidation: a background poller (every scopePollInterval)
// computes a cheap fingerprint of the six tables (count, max(updated_at),
// max(deleted_at)) plus a hash of the rule-relevant credential attributes and
// rebuilds when it changes. Management writes on this replica invalidate
// immediately. Worst-case staleness on other replicas = one poll interval.

const (
	scopePollInterval     = 2 * time.Second
	scopeMissRebuildEvery = time.Second
)

type scopeKey struct {
	ID          uint
	Channels    []uint
	ModelGroups []uint
	UserID      *uint
}

type scopeModelDetail struct {
	ModelID  string // canonical
	Key      string // lower canonical
	Channels []uint
}

// ScopeSnapshot is an immutable view used for dispatch and panel summaries.
type ScopeSnapshot struct {
	BuiltAt        time.Time
	keys           map[string]*scopeKey
	keyByID        map[uint]*scopeKey
	ChannelGroups  map[uint]ChannelGroupRecord
	ModelGroups    map[uint]ModelGroupRecord
	StaticMembers  map[uint][]string // all groups, enabled or not
	RuleMembers    map[uint][]string
	Rules          map[uint]ChannelGroupRuleRecord
	modelDetails   map[uint][]scopeModelDetail // all model groups
	enabledMembers map[uint][]string           // enabled channel groups: static ∪ rule
	BuildDuration  time.Duration
	fingerprint    string
	KeyCount       int
}

// Members returns effective members (static ∪ rule) of a group, regardless of enabled state.
func (s *ScopeSnapshot) Members(groupID uint) []string {
	return unionStrings(s.StaticMembers[groupID], s.RuleMembers[groupID])
}

// KeysBoundTo counts keys bound to a credential group / model group.
func (s *ScopeSnapshot) KeysBoundTo() (byChannel map[uint]int, byModelGroup map[uint]int, unrestricted int) {
	byChannel, byModelGroup = map[uint]int{}, map[uint]int{}
	for _, k := range s.keyByID {
		for _, c := range k.Channels {
			byChannel[c]++
		}
		for _, m := range k.ModelGroups {
			byModelGroup[m]++
		}
		if len(k.Channels) == 0 && len(k.ModelGroups) == 0 {
			unrestricted++
		}
	}
	return
}

// GroupsOfAuth maps auth id -> effective group ids.
func (s *ScopeSnapshot) GroupsOfAuth() map[string][]uint {
	out := map[string][]uint{}
	for gid := range s.ChannelGroups {
		for _, a := range s.Members(gid) {
			out[a] = append(out[a], gid)
		}
	}
	for a := range out {
		sort.Slice(out[a], func(i, j int) bool { return out[a][i] < out[a][j] })
	}
	return out
}

// ModelGroupModels returns the canonical model ids of a model group.
func (s *ScopeSnapshot) ModelGroupModels(groupID uint) []string {
	var out []string
	for _, d := range s.modelDetails[groupID] {
		out = append(out, d.ModelID)
	}
	return out
}

type scopeCache struct {
	repo       *Repository
	snap       atomic.Pointer[ScopeSnapshot]
	dirty      atomic.Bool
	buildMu    sync.Mutex
	lastMiss   atomic.Int64
	started    atomic.Bool
	authLister atomic.Value // func() []*coreauth.Auth
	ruleMu     sync.Mutex
	authStats  string
	ruleHashV  string
}

var (
	scopeCachesMu sync.Mutex
	scopeCaches   []*scopeCache
)

// InvalidateScopeCaches marks every scope cache in this process stale (called
// after management writes).
func InvalidateScopeCaches() {
	scopeCachesMu.Lock()
	defer scopeCachesMu.Unlock()
	for _, c := range scopeCaches {
		c.dirty.Store(true)
	}
}

func (r *Repository) scopeCache() *scopeCache {
	r.scopeOnce.Do(func() {
		r.scope = &scopeCache{repo: r}
		scopeCachesMu.Lock()
		scopeCaches = append(scopeCaches, r.scope)
		scopeCachesMu.Unlock()
	})
	return r.scope
}

// SetAuthLister provides the live credential list used to evaluate group rules
// (defaults to reading the auth table).
func (r *Repository) SetAuthLister(fn func() []*coreauth.Auth) {
	if r != nil && fn != nil {
		r.scopeCache().authLister.Store(fn)
	}
}

// StartScopeCache starts the background fingerprint poller.
func (r *Repository) StartScopeCache(ctx context.Context) {
	c := r.scopeCache()
	if !c.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		t := time.NewTicker(scopePollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := c.refreshIfChanged(ctx); err != nil {
					log.Debugf("scope cache refresh: %v", err)
				}
			}
		}
	}()
}

// Scope returns the current snapshot, building it when missing or invalidated.
func (r *Repository) Scope(ctx context.Context) (*ScopeSnapshot, error) {
	c := r.scopeCache()
	s := c.snap.Load()
	if s != nil && !c.dirty.Load() {
		return s, nil
	}
	c.dirty.Store(true)
	return c.rebuild(ctx)
}

func (c *scopeCache) listAuths(ctx context.Context) []*coreauth.Auth {
	if fn, ok := c.authLister.Load().(func() []*coreauth.Auth); ok && fn != nil {
		return fn()
	}
	auths, err := c.repo.listRuleAuths(ctx)
	if err != nil {
		log.Debugf("scope cache: list credentials for rules: %v", err)
		return nil
	}
	return auths
}

// listRuleAuths loads only the credential attributes group rules look at
// (provider, label, e-mail, tags, priority) from the auth table.
func (r *Repository) listRuleAuths(ctx context.Context) ([]*coreauth.Auth, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		UUID     string
		Provider string
		Label    string
		AuthJSON JSONB
	}
	if err := db.WithContext(contextOrBackground(ctx)).Model(&AuthRecord{}).Select("uuid, provider, label, auth_json").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*coreauth.Auth, 0, len(rows))
	for _, row := range rows {
		var doc struct {
			Metadata   map[string]any    `json:"metadata"`
			Attributes map[string]string `json:"attributes"`
		}
		_ = json.Unmarshal(row.AuthJSON, &doc)
		md := map[string]any{}
		for _, k := range []string{"email", "tags", "priority"} {
			if v, ok := doc.Metadata[k]; ok {
				md[k] = v
			}
		}
		attrs := map[string]string{}
		for _, k := range []string{"email", "priority"} {
			if v, ok := doc.Attributes[k]; ok {
				attrs[k] = v
			}
		}
		out = append(out, &coreauth.Auth{ID: row.UUID, Provider: row.Provider, Label: row.Label, Metadata: md, Attributes: attrs})
	}
	return out, nil
}

func (c *scopeCache) refreshIfChanged(ctx context.Context) error {
	cur := c.snap.Load()
	if cur == nil {
		c.dirty.Store(true)
		_, err := c.rebuild(ctx)
		return err
	}
	fp, err := c.fingerprint(ctx, len(cur.Rules) > 0)
	if err != nil {
		return err
	}
	if fp != cur.fingerprint {
		c.dirty.Store(true)
		_, err = c.rebuild(ctx)
	}
	return err
}

var scopeTables = []string{"api_key", "channel_group", "channel_group_detail", "model_group", "model_group_detail", "channel_group_rule"}

func (c *scopeCache) fingerprint(ctx context.Context, withRules bool) (string, error) {
	db, err := c.repo.database()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(scopeTables))
	for _, t := range scopeTables {
		del := "MAX(deleted_at)"
		if t == "channel_group_rule" {
			del = "''"
		}
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS t, COUNT(*) AS n, CAST(MAX(updated_at) AS TEXT) AS u, CAST(%s AS TEXT) AS d FROM %s", t, del, t))
	}
	var rows []struct {
		T string
		N int64
		U *string
		D *string
	}
	if err := db.WithContext(contextOrBackground(ctx)).Raw(strings.Join(parts, " UNION ALL ")).Scan(&rows).Error; err != nil {
		return "", err
	}
	h := sha256.New()
	for _, row := range rows {
		fmt.Fprintf(h, "%s|%d|%s|%s;", row.T, row.N, deref(row.U), deref(row.D))
	}
	if withRules {
		h.Write([]byte(c.ruleHash(ctx, db)))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ruleHash returns a hash of the rule-relevant credential attributes. The
// (cheap) auth table stats are checked first; credentials are only re-read
// when the table changed.
func (c *scopeCache) ruleHash(ctx context.Context, db *gorm.DB) string {
	var st struct {
		N int64
		U *string
		D *string
	}
	_ = db.WithContext(contextOrBackground(ctx)).Raw("SELECT COUNT(*) AS n, CAST(MAX(updated_at) AS TEXT) AS u, CAST(MAX(deleted_at) AS TEXT) AS d FROM auth").Scan(&st).Error
	stats := fmt.Sprintf("%d|%s|%s", st.N, deref(st.U), deref(st.D))
	c.ruleMu.Lock()
	defer c.ruleMu.Unlock()
	if _, custom := c.authLister.Load().(func() []*coreauth.Auth); !custom && stats == c.authStats && c.ruleHashV != "" {
		return c.ruleHashV
	}
	c.authStats = stats
	c.ruleHashV = authRuleFingerprint(c.listAuths(ctx))
	return c.ruleHashV
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// authRuleFingerprint hashes only the credential attributes rules look at.
func authRuleFingerprint(auths []*coreauth.Auth) string {
	h := sha256.New()
	ids := make([]string, 0, len(auths))
	byID := map[string]*coreauth.Auth{}
	for _, a := range auths {
		if a != nil {
			ids = append(ids, a.ID)
			byID[a.ID] = a
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := byID[id]
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%d;", id, a.Provider, a.Label, CredentialEmail(a), strings.Join(CredentialTags(a), ","), CredentialPriority(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *scopeCache) rebuild(ctx context.Context) (*ScopeSnapshot, error) {
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	if s := c.snap.Load(); s != nil && !c.dirty.Load() {
		return s, nil // built by a concurrent caller
	}
	c.dirty.Store(false)
	start := time.Now()
	db, err := c.repo.database()
	if err != nil {
		return nil, err
	}
	if err := ensureChannelGroupRuleTable(ctx, db); err != nil {
		return nil, err
	}
	rules, err := c.repo.ListChannelGroupRules(ctx)
	if err != nil {
		return nil, err
	}
	fp, err := c.fingerprint(ctx, len(rules) > 0)
	if err != nil {
		return nil, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	var keys []APIKeyRecord
	var cgs []ChannelGroupRecord
	var cgds []ChannelGroupDetailRecord
	var mgs []ModelGroupRecord
	var mgds []ModelGroupDetailRecord
	for _, q := range []struct {
		dst   any
		order string
	}{{&keys, "id"}, {&cgs, "id"}, {&cgds, "channel_group_id, id"}, {&mgs, "id"}, {&mgds, "model_group_id, id"}} {
		if err := tx.Order(q.order).Find(q.dst).Error; err != nil {
			c.dirty.Store(true)
			return nil, err
		}
	}
	s := &ScopeSnapshot{
		keys: make(map[string]*scopeKey, len(keys)), keyByID: make(map[uint]*scopeKey, len(keys)),
		ChannelGroups: map[uint]ChannelGroupRecord{}, ModelGroups: map[uint]ModelGroupRecord{},
		StaticMembers: map[uint][]string{}, RuleMembers: map[uint][]string{}, Rules: rules,
		modelDetails: map[uint][]scopeModelDetail{}, enabledMembers: map[uint][]string{}, fingerprint: fp, KeyCount: len(keys),
	}
	for i := range keys {
		ch, errC := apiKeyChannelsFromJSON(keys[i].Channels)
		mg, errM := apiKeyModelGroupsFromJSON(keys[i].ModelGroups)
		if errC != nil || errM != nil {
			continue // malformed row: lookups fall back to the database path
		}
		k := &scopeKey{ID: keys[i].ID, Channels: normalizeChannelGroupIDs(ch), ModelGroups: normalizeChannelGroupIDs(mg), UserID: keys[i].UserID}
		s.keys[strings.TrimSpace(keys[i].APIKey)] = k
		s.keyByID[k.ID] = k
	}
	for _, g := range cgs {
		s.ChannelGroups[g.ID] = g
	}
	for _, g := range mgs {
		s.ModelGroups[g.ID] = g
	}
	seen := map[uint]map[string]bool{}
	for _, d := range cgds {
		if _, ok := s.ChannelGroups[d.ChannelGroupID]; !ok {
			continue
		}
		id := strings.TrimSpace(d.AuthID)
		if id == "" {
			continue
		}
		if seen[d.ChannelGroupID] == nil {
			seen[d.ChannelGroupID] = map[string]bool{}
		}
		if !seen[d.ChannelGroupID][id] {
			seen[d.ChannelGroupID][id] = true
			s.StaticMembers[d.ChannelGroupID] = append(s.StaticMembers[d.ChannelGroupID], id)
		}
	}
	if len(rules) > 0 {
		auths := c.listAuths(ctx)
		sort.Slice(auths, func(i, j int) bool { return auths[i] != nil && auths[j] != nil && auths[i].ID < auths[j].ID })
		for gid, rec := range rules {
			if _, ok := s.ChannelGroups[gid]; !ok || !rec.Enabled {
				continue
			}
			rule, errRule := rec.ParsedRule()
			if errRule != nil {
				continue
			}
			for _, a := range auths {
				if a != nil && rule.Matches(a) {
					s.RuleMembers[gid] = append(s.RuleMembers[gid], a.ID)
				}
			}
		}
	}
	for gid, g := range s.ChannelGroups {
		if !g.Disabled {
			s.enabledMembers[gid] = s.Members(gid)
		}
	}
	for _, d := range mgds {
		if _, ok := s.ModelGroups[d.ModelGroupID]; !ok {
			continue
		}
		mid := canonicalModelGroupModelID(d.ModelID)
		if mid == "" {
			continue
		}
		chans, _ := ModelGroupDetailChannelIDs(&d)
		s.modelDetails[d.ModelGroupID] = append(s.modelDetails[d.ModelGroupID], scopeModelDetail{ModelID: mid, Key: strings.ToLower(mid), Channels: normalizeChannelGroupIDs(chans)})
	}
	s.BuiltAt = time.Now()
	s.BuildDuration = time.Since(start)
	c.snap.Store(s)
	return s, nil
}

func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

var errScopeKeyMissing = errors.New("api key not in scope snapshot")

// lookupKey returns the key from the snapshot, rebuilding at most once per
// second for keys created after the last build.
func (r *Repository) scopeLookupKey(ctx context.Context, apiKey string) (*ScopeSnapshot, *scopeKey, error) {
	s, err := r.Scope(ctx)
	if err != nil {
		return nil, nil, err
	}
	if k, ok := s.keys[apiKey]; ok {
		return s, k, nil
	}
	c := r.scopeCache()
	now := time.Now().UnixNano()
	if last := c.lastMiss.Load(); now-last > int64(scopeMissRebuildEvery) && c.lastMiss.CompareAndSwap(last, now) {
		c.dirty.Store(true)
		if s, err = c.rebuild(ctx); err != nil {
			return nil, nil, err
		}
		if k, ok := s.keys[apiKey]; ok {
			return s, k, nil
		}
	}
	return s, nil, errScopeKeyMissing
}

// unionMembers returns the effective members of the enabled groups in ids.
func (s *ScopeSnapshot) unionMembers(ids []uint) []string {
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, gid := range normalizeChannelGroupIDs(ids) {
		for _, a := range s.enabledMembers[gid] {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

func (s *ScopeSnapshot) allowedModels(k *scopeKey) ([]string, []scopeModelDetail) {
	if len(k.ModelGroups) == 0 {
		return nil, nil
	}
	var details []scopeModelDetail
	for _, gid := range k.ModelGroups {
		if g, ok := s.ModelGroups[gid]; ok && !g.Disabled {
			details = append(details, s.modelDetails[gid]...)
		}
	}
	ids := make([]string, 0, len(details))
	seen := map[string]bool{}
	for _, d := range details {
		if !seen[d.Key] {
			seen[d.Key] = true
			ids = append(ids, d.ModelID)
		}
	}
	return ids, details
}

// CachedAllowedDispatchIDsForAPIKeyModel is the cached equivalent of
// AllowedDispatchIDsForAPIKeyModel (plus rule-based members).
func (r *Repository) CachedAllowedDispatchIDsForAPIKeyModel(ctx context.Context, apiKey, modelID string) ([]string, []string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, nil, nil
	}
	s, k, err := r.scopeLookupKey(ctx, apiKey)
	if errors.Is(err, errScopeKeyMissing) {
		return r.AllowedDispatchIDsForAPIKeyModel(ctx, apiKey, modelID)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := r.scopeBilling(ctx, k); err != nil {
		return nil, nil, err
	}
	modelIDs, details := s.allowedModels(k)
	var authIDs []string
	if len(k.Channels) > 0 {
		authIDs = s.unionMembers(k.Channels)
	}
	modelKey := strings.ToLower(canonicalModelGroupModelID(strings.TrimSpace(modelID)))
	if modelKey == "" {
		return authIDs, modelIDs, nil
	}
	var modelChannels []uint
	restricted := false
	for _, d := range details {
		if d.Key == modelKey && len(d.Channels) > 0 {
			restricted = true
			modelChannels = append(modelChannels, d.Channels...)
		}
	}
	if !restricted {
		return authIDs, modelIDs, nil
	}
	return intersectAllowedAuthIDs(authIDs, s.unionMembers(modelChannels)), modelIDs, nil
}

// CachedAllowedAuthIDsForAPIKey is the cached AllowedAuthIDsForAPIKey.
func (r *Repository) CachedAllowedAuthIDsForAPIKey(ctx context.Context, apiKey string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, nil
	}
	s, k, err := r.scopeLookupKey(ctx, apiKey)
	if errors.Is(err, errScopeKeyMissing) {
		return r.AllowedAuthIDsForAPIKey(ctx, apiKey)
	}
	if err != nil {
		return nil, err
	}
	if len(k.Channels) == 0 {
		return nil, nil
	}
	return s.unionMembers(k.Channels), nil
}

// CachedAllowedModelIDsForAPIKey is the cached AllowedModelIDsForAPIKey.
func (r *Repository) CachedAllowedModelIDsForAPIKey(ctx context.Context, apiKey string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, nil
	}
	s, k, err := r.scopeLookupKey(ctx, apiKey)
	if errors.Is(err, errScopeKeyMissing) {
		return r.AllowedModelIDsForAPIKey(ctx, apiKey)
	}
	if err != nil {
		return nil, err
	}
	ids, _ := s.allowedModels(k)
	return ids, nil
}

// scopeBilling keeps the per-user credit check (only for keys owned by a user).
func (r *Repository) scopeBilling(ctx context.Context, k *scopeKey) error {
	if k == nil || k.UserID == nil {
		return nil
	}
	db, err := r.database()
	if err != nil {
		return err
	}
	return ensureAPIKeyUserBillingAllowed(ctx, db, &APIKeyRecord{ID: k.ID, UserID: k.UserID})
}

var _ = gorm.ErrRecordNotFound

// PollScopeOnce runs one background change check (used by benchmarks).
func (r *Repository) PollScopeOnce(ctx context.Context) error {
	return r.scopeCache().refreshIfChanged(ctx)
}
