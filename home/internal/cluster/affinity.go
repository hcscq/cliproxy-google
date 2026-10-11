package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sticky sessions: affinity key -> credential bindings shared by all Home
// replicas through the database, plus a per-request outcome log used by the
// panel (hit rate, request detail).

// AffinityBindingRecord is one key -> credential binding.
type AffinityBindingRecord struct {
	BindingKey string    `gorm:"column:binding_key;primaryKey;size:64"`
	AuthID     string    `gorm:"column:auth_id;not null"`
	Model      string    `gorm:"column:model;not null;default:''"`
	Source     string    `gorm:"column:source;not null;default:''"`
	ExpiresAt  time.Time `gorm:"column:expires_at;not null;index"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

// TableName returns the table name.
func (AffinityBindingRecord) TableName() string { return "session_affinity" }

// RequestAffinityRecord stores the sticky-routing outcome of one request.
type RequestAffinityRecord struct {
	RequestID string    `gorm:"column:request_id;primaryKey;size:128"`
	Source    string    `gorm:"column:source;not null;default:''"`
	Outcome   string    `gorm:"column:outcome;not null;default:''"` // hit | new | repin | none
	AuthID    string    `gorm:"column:auth_id;not null;default:''"`
	PrevAuth  string    `gorm:"column:prev_auth_id;not null;default:''"`
	CreatedAt time.Time `gorm:"column:created_at;not null;index"`
}

// TableName returns the table name.
func (RequestAffinityRecord) TableName() string { return "request_affinity" }

// Affinity outcomes.
const (
	AffinityHit   = "hit"
	AffinityNew   = "new"
	AffinityRepin = "repin"
	AffinityNone  = "none"
)

func ensureAffinityTables(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	for _, m := range []any{&AffinityBindingRecord{}, &RequestAffinityRecord{}} {
		if !tx.Migrator().HasTable(m) {
			if err := tx.Migrator().CreateTable(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// AffinityBindingKey scopes a proxy affinity key to the caller's API key.
func AffinityBindingKey(apiKey, affinityKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey) + "\x00" + strings.TrimSpace(affinityKey)))
	return hex.EncodeToString(sum[:])
}

type affinityEntry struct {
	authID  string
	fetched time.Time
	written time.Time
}

// affinityCache is a small per-replica read-through cache. Entries live for
// affinityCacheTTL; a stale entry can only cause one extra pinned attempt that
// fails over, so cross-replica invalidation is not needed beyond expiry.
type affinityCache struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
}

const (
	affinityCacheTTL   = 15 * time.Second
	affinityCacheLimit = 50000
)

var affinityMem = &affinityCache{entries: map[string]affinityEntry{}}

func (c *affinityCache) get(key string, now time.Time) (affinityEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.fetched) > affinityCacheTTL {
		return affinityEntry{}, false
	}
	return e, true
}

func (c *affinityCache) put(key string, e affinityEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= affinityCacheLimit {
		c.entries = map[string]affinityEntry{}
	}
	c.entries[key] = e
}

// LookupAffinity returns the bound credential for a binding key, if any.
func (r *Repository) LookupAffinity(ctx context.Context, bindingKey string) (string, bool) {
	if r == nil || r.db == nil || bindingKey == "" {
		return "", false
	}
	now := time.Now()
	if e, ok := affinityMem.get(bindingKey, now); ok {
		return e.authID, e.authID != ""
	}
	var rec AffinityBindingRecord
	err := r.db.WithContext(contextOrBackground(ctx)).Where("binding_key = ? AND expires_at > ?", bindingKey, now).Take(&rec).Error
	if err != nil {
		affinityMem.put(bindingKey, affinityEntry{fetched: now})
		return "", false
	}
	affinityMem.put(bindingKey, affinityEntry{authID: rec.AuthID, fetched: now, written: rec.UpdatedAt})
	return rec.AuthID, true
}

// BindAffinity upserts a binding and extends its TTL. Refreshing an unchanged
// binding is throttled so hot conversations do not write on every request.
func (r *Repository) BindAffinity(ctx context.Context, bindingKey, authID, model, source string, ttl time.Duration) error {
	if r == nil || r.db == nil || bindingKey == "" || authID == "" {
		return nil
	}
	now := time.Now()
	if e, ok := affinityMem.get(bindingKey, now); ok && e.authID == authID && now.Sub(e.written) < ttl/10 {
		return nil
	}
	rec := AffinityBindingRecord{BindingKey: bindingKey, AuthID: authID, Model: model, Source: source, ExpiresAt: now.Add(ttl), UpdatedAt: now}
	err := r.db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "binding_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"auth_id", "model", "source", "expires_at", "updated_at"}),
	}).Create(&rec).Error
	if err == nil {
		affinityMem.put(bindingKey, affinityEntry{authID: authID, fetched: now, written: now})
	}
	return err
}

// RecordRequestAffinity queues a request's sticky outcome for a batched,
// bounded background writer (best effort). It never blocks the request path
// and never spawns per-request goroutines: under load those exhausted the DB
// pool and starved the leader lease (Home got fenced in capacity tests).
func (r *Repository) RecordRequestAffinity(requestID, source, outcome, authID, prevAuth string) {
	if r == nil || r.db == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	rec := RequestAffinityRecord{RequestID: strings.TrimSpace(requestID), Source: source, Outcome: outcome, AuthID: authID, PrevAuth: prevAuth, CreatedAt: time.Now()}
	r.affinityWriterOnce.Do(func() {
		r.affinityQueue = make(chan RequestAffinityRecord, affinityQueueSize)
		go r.runAffinityWriter()
	})
	select {
	case r.affinityQueue <- rec:
	default:
		affinityDropped.Add(1)
	}
}

const (
	affinityQueueSize  = 20000
	affinityBatchSize  = 500
	affinityFlushEvery = 500 * time.Millisecond
)

var affinityDropped atomic.Int64

// AffinityRecordsDropped reports outcome rows dropped because the queue was full.
func AffinityRecordsDropped() int64 { return affinityDropped.Load() }

func (r *Repository) runAffinityWriter() {
	t := time.NewTicker(affinityFlushEvery)
	defer t.Stop()
	batch := make([]RequestAffinityRecord, 0, affinityBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(batch, affinityBatchSize).Error; err != nil {
			log.WithError(err).Debug("record request affinity batch failed")
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case rec := <-r.affinityQueue:
			batch = append(batch, rec)
			if len(batch) >= affinityBatchSize {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// RequestAffinity returns the recorded outcome of one request.
func (r *Repository) RequestAffinity(ctx context.Context, requestIDs ...string) (*RequestAffinityRecord, bool) {
	if r == nil || r.db == nil {
		return nil, false
	}
	for _, id := range requestIDs {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		var rec RequestAffinityRecord
		if err := r.db.WithContext(contextOrBackground(ctx)).Where("request_id = ?", id).Take(&rec).Error; err == nil {
			return &rec, true
		}
	}
	return nil, false
}

// AffinityStats counts outcomes since a time.
type AffinityStats struct {
	Total          int64            `json:"total"`
	WithKey        int64            `json:"with_key"`
	Hits           int64            `json:"hits"`
	New            int64            `json:"new"`
	Repins         int64            `json:"repins"`
	None           int64            `json:"none"`
	HitRate        float64          `json:"hit_rate"`
	BySource       map[string]int64 `json:"by_source"`
	ActiveBindings int64            `json:"active_bindings"`
}

// AffinityStatsSince aggregates request outcomes.
func (r *Repository) AffinityStatsSince(ctx context.Context, since time.Time) (AffinityStats, error) {
	out := AffinityStats{BySource: map[string]int64{}}
	if r == nil || r.db == nil {
		return out, nil
	}
	db := r.db.WithContext(contextOrBackground(ctx))
	type row struct {
		Source  string
		Outcome string
		N       int64
	}
	var rows []row
	if err := db.Model(&RequestAffinityRecord{}).Select("source, outcome, count(*) as n").Where("created_at >= ?", since).Group("source, outcome").Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, x := range rows {
		out.Total += x.N
		switch x.Outcome {
		case AffinityHit:
			out.Hits += x.N
		case AffinityNew:
			out.New += x.N
		case AffinityRepin:
			out.Repins += x.N
		default:
			out.None += x.N
		}
		if x.Outcome != AffinityNone && x.Source != "" {
			out.BySource[x.Source] += x.N
		}
	}
	out.WithKey = out.Hits + out.New + out.Repins
	if out.WithKey > 0 {
		out.HitRate = float64(out.Hits) / float64(out.WithKey)
	}
	_ = db.Model(&AffinityBindingRecord{}).Where("expires_at > ?", time.Now()).Count(&out.ActiveBindings).Error
	return out, nil
}

// PurgeAffinity removes expired bindings and old outcome rows.
func (r *Repository) PurgeAffinity(ctx context.Context, keep time.Duration) {
	if r == nil || r.db == nil {
		return
	}
	db := r.db.WithContext(contextOrBackground(ctx))
	now := time.Now()
	_ = db.Where("expires_at < ?", now).Delete(&AffinityBindingRecord{}).Error
	_ = db.Where("created_at < ?", now.Add(-keep)).Delete(&RequestAffinityRecord{}).Error
}

// StartAffinityJanitor purges expired rows every 10 minutes (idempotent on all replicas).
func (r *Repository) StartAffinityJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.PurgeAffinity(ctx, 7*24*time.Hour)
			}
		}
	}()
}
