package home

import (
	"strings"
	"sync"
	"time"
)

// CatalogModelQuota is one credential's catalog quotaInfo for a single model.
// Remaining == nil means the catalog entry has no quotaInfo (treated as fully
// remaining, matching ReserveModelCapacity). A non-nil 0 means quotaInfo was
// present but reported no remaining fraction.
type CatalogModelQuota struct {
	Remaining *float64
	Reset     time.Time
}

// antigravityModelCatalog is an incremental reverse index of the in-memory
// Antigravity catalogs: model → credential → quota. Credential search reads it
// instead of parsing every catalog blob on the request path. Updates are
// O(models of the one credential that changed).
type modelCatalogIndex struct {
	mu      sync.RWMutex
	byModel map[string]map[string]CatalogModelQuota // lower(model) → auth → quota
	display map[string]string                       // lower(model) → first seen id
	nByID   map[string]int                          // display id → credentials
	byAuth  map[string]map[string]struct{}          // auth → lower(model) set
}

func newModelCatalogIndex() *modelCatalogIndex {
	return &modelCatalogIndex{
		byModel: map[string]map[string]CatalogModelQuota{},
		display: map[string]string{},
		nByID:   map[string]int{},
		byAuth:  map[string]map[string]struct{}{},
	}
}

var antigravityModelCatalog = newModelCatalogIndex()

func noteAntigravityModelCatalog(authID string, health *antigravityCatalogHealth) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if health == nil || len(health.models) == 0 {
		antigravityModelCatalog.forget(authID)
		return
	}
	antigravityModelCatalog.replace(authID, health)
}

func forgetAntigravityModelCatalog(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	antigravityModelCatalog.forget(authID)
}

// AntigravityModelCatalogCounts returns model id → number of credentials whose
// latest cached catalog lists that model. Placeholder "*-disabled" keys are
// omitted. The map is a copy.
func AntigravityModelCatalogCounts() map[string]int {
	return antigravityModelCatalog.counts()
}

// AntigravityModelCatalogQuotas returns auth id → quota for one model id
// (case-insensitive, not rewritten). nil when no cached catalog lists it.
// The map is a copy.
func AntigravityModelCatalogQuotas(model string) map[string]CatalogModelQuota {
	return antigravityModelCatalog.quotas(model)
}

func (idx *modelCatalogIndex) replace(authID string, health *antigravityCatalogHealth) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.removeLocked(authID)
	next := map[string]struct{}{}
	for key, m := range health.models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" || strings.ContainsAny(id, "*?") || catalogModelPlaceholder(id) || catalogModelPlaceholder(key) {
			continue
		}
		low := strings.ToLower(id)
		next[low] = struct{}{}
		bucket := idx.byModel[low]
		if bucket == nil {
			bucket = map[string]CatalogModelQuota{}
			idx.byModel[low] = bucket
		}
		bucket[authID] = catalogQuotaOf(m)
		if _, ok := idx.display[low]; !ok {
			idx.display[low] = id
		}
		idx.nByID[idx.display[low]]++
	}
	if len(next) == 0 {
		delete(idx.byAuth, authID)
		return
	}
	idx.byAuth[authID] = next
}

func (idx *modelCatalogIndex) forget(authID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.removeLocked(authID)
}

func (idx *modelCatalogIndex) removeLocked(authID string) {
	prev := idx.byAuth[authID]
	delete(idx.byAuth, authID)
	for low := range prev {
		bucket := idx.byModel[low]
		delete(bucket, authID)
		if d := idx.display[low]; d != "" {
			idx.nByID[d]--
			if idx.nByID[d] <= 0 {
				delete(idx.nByID, d)
			}
		}
		if len(bucket) == 0 {
			delete(idx.byModel, low)
			delete(idx.display, low)
		}
	}
}

func (idx *modelCatalogIndex) counts() map[string]int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string]int, len(idx.nByID))
	for id, n := range idx.nByID {
		if n > 0 {
			out[id] = n
		}
	}
	return out
}

func (idx *modelCatalogIndex) quotas(model string) map[string]CatalogModelQuota {
	low := strings.ToLower(strings.TrimSpace(model))
	if low == "" {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	src := idx.byModel[low]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]CatalogModelQuota, len(src))
	for id, q := range src {
		out[id] = q
	}
	return out
}

func catalogModelPlaceholder(id string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(id)), "-disabled")
}

func catalogQuotaOf(m CatalogModelHealth) CatalogModelQuota {
	q := CatalogModelQuota{}
	if m.QuotaRemaining != nil {
		v := *m.QuotaRemaining
		q.Remaining = &v
	}
	if ts := strings.TrimSpace(m.QuotaResetTime); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			q.Reset = t
		} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
			q.Reset = t
		}
	}
	return q
}
