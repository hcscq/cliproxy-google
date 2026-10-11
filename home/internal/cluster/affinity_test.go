package cluster

import (
	"testing"
	"time"
)

func TestAffinityBindLookupExpiryAndStats(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	if err := ensureAffinityTables(ctx, repo.db); err != nil {
		t.Fatal(err)
	}
	k := AffinityBindingKey("key-1", "conv-a")
	if k == AffinityBindingKey("key-2", "conv-a") {
		t.Fatal("binding key must be scoped to the API key")
	}
	if _, ok := repo.LookupAffinity(ctx, k); ok {
		t.Fatal("unexpected binding")
	}
	affinityMem.put(k, affinityEntry{}) // drop negative cache entry
	affinityMem.mu.Lock()
	delete(affinityMem.entries, k)
	affinityMem.mu.Unlock()
	if err := repo.BindAffinity(ctx, k, "auth-1", "gemini-3-flash", "prefix", time.Hour); err != nil {
		t.Fatal(err)
	}
	// Fresh replica view: bypass the in-memory cache.
	affinityMem.mu.Lock()
	affinityMem.entries = map[string]affinityEntry{}
	affinityMem.mu.Unlock()
	if got, ok := repo.LookupAffinity(ctx, k); !ok || got != "auth-1" {
		t.Fatalf("lookup = %q %v", got, ok)
	}
	// Re-pin overwrites.
	if err := repo.BindAffinity(ctx, k, "auth-2", "gemini-3-flash", "prefix", time.Hour); err != nil {
		t.Fatal(err)
	}
	affinityMem.mu.Lock()
	affinityMem.entries = map[string]affinityEntry{}
	affinityMem.mu.Unlock()
	if got, _ := repo.LookupAffinity(ctx, k); got != "auth-2" {
		t.Fatalf("after repin = %q", got)
	}
	// Expired bindings are ignored and purged.
	exp := AffinityBindingKey("key-1", "old")
	if err := repo.db.Create(&AffinityBindingRecord{BindingKey: exp, AuthID: "auth-x", ExpiresAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.LookupAffinity(ctx, exp); ok {
		t.Fatal("expired binding returned")
	}
	repo.PurgeAffinity(ctx, time.Hour)
	var n int64
	repo.db.Model(&AffinityBindingRecord{}).Where("binding_key = ?", exp).Count(&n)
	if n != 0 {
		t.Fatal("expired binding not purged")
	}
	// Stats.
	for i, o := range []string{AffinityHit, AffinityHit, AffinityHit, AffinityNew, AffinityRepin, AffinityNone} {
		rec := RequestAffinityRecord{RequestID: "r" + string(rune('a'+i)), Source: "prefix", Outcome: o, AuthID: "auth-1", CreatedAt: time.Now()}
		if o == AffinityNone {
			rec.Source = ""
		}
		if err := repo.db.Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
	}
	st, err := repo.AffinityStatsSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 6 || st.WithKey != 5 || st.Hits != 3 || st.HitRate != 0.6 || st.BySource["prefix"] != 5 || st.ActiveBindings != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if rec, ok := repo.RequestAffinity(ctx, "", "ra"); !ok || rec.Outcome != AffinityHit {
		t.Fatalf("request affinity = %+v %v", rec, ok)
	}
}
