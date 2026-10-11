package cluster

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func newScopeTestRepo(t testing.TB) *Repository {
	t.Helper()
	db, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewRepository(db)
}

func sorted(in []string) []string {
	if in == nil {
		return nil
	}
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Without rules the cache must return exactly what the database path returns.
func TestScopeCacheMatchesDatabasePath(t *testing.T) {
	repo := newScopeTestRepo(t)
	ctx := context.Background()
	res, err := repo.SeedSynthetic(ctx, SyntheticScale{Credentials: 60, Models: 40, Keys: 120, ChannelGroups: 12, ModelGroups: 6, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	// drop rules for the strict comparison (the DB path does not know them)
	for _, g := range res.Channels {
		if err := repo.PutChannelGroupRule(ctx, g, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	InvalidateScopeCaches()
	models := append([]string{""}, res.Models...)
	for _, key := range res.APIKeys {
		for _, m := range models[:15] {
			wantA, wantM, err1 := repo.AllowedDispatchIDsForAPIKeyModel(ctx, key, m)
			gotA, gotM, err2 := repo.CachedAllowedDispatchIDsForAPIKeyModel(ctx, key, m)
			if (err1 == nil) != (err2 == nil) || !reflect.DeepEqual(sorted(wantA), sorted(gotA)) || !reflect.DeepEqual(sorted(wantM), sorted(gotM)) {
				t.Fatalf("key %s model %q: db=(%v,%v,%v) cache=(%v,%v,%v)", key, m, wantA, wantM, err1, gotA, gotM, err2)
			}
		}
		wa, _ := repo.AllowedAuthIDsForAPIKey(ctx, key)
		ga, _ := repo.CachedAllowedAuthIDsForAPIKey(ctx, key)
		if !reflect.DeepEqual(sorted(wa), sorted(ga)) {
			t.Fatalf("auth ids differ for %s", key)
		}
	}
}

func TestScopeCacheRulesAndInvalidation(t *testing.T) {
	repo := newScopeTestRepo(t)
	ctx := context.Background()
	res, err := repo.SeedSynthetic(ctx, SyntheticScale{Credentials: 50, Models: 5, Keys: 2, ChannelGroups: 3, ModelGroups: 1, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.Scope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// group 0 rule: *@corp-a.example
	for _, id := range s.RuleMembers[res.Channels[0]] {
		var a AuthRecord
		if err := repo.db.Where("uuid = ?", id).First(&a).Error; err != nil {
			t.Fatal(err)
		}
		if EmailDomain(a.Label) != "corp-a.example" {
			t.Fatalf("rule matched %s", a.Label)
		}
	}
	if len(s.RuleMembers[res.Channels[0]]) == 0 {
		t.Fatal("expected rule members")
	}
	// fingerprint detects a change made "by another replica" (no explicit invalidate)
	c := repo.scopeCache()
	if err := c.refreshIfChanged(ctx); err != nil {
		t.Fatal(err)
	}
	before := c.snap.Load()
	if err := repo.db.Create(&ChannelGroupDetailRecord{ChannelGroupID: res.Channels[1], AuthID: "new-member"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := c.refreshIfChanged(ctx); err != nil {
		t.Fatal(err)
	}
	after := c.snap.Load()
	if after == before {
		t.Fatal("snapshot must rebuild after a membership change")
	}
	found := false
	for _, m := range after.Members(res.Channels[1]) {
		found = found || m == "new-member"
	}
	if !found {
		t.Fatal("new member missing")
	}
}

func TestScopeRuleTagsFromDatabaseAndAuthChange(t *testing.T) {
	repo := newScopeTestRepo(t)
	ctx := context.Background()
	res, err := repo.SeedSynthetic(ctx, SyntheticScale{Credentials: 40, Models: 2, Keys: 1, ChannelGroups: 2, ModelGroups: 0, Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	vipGroup := res.Channels[1] // seeded rule: tags_any vip
	s, err := repo.Scope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.RuleMembers[vipGroup])
	// tag a credential that is not yet a member; the poller must notice without an explicit invalidation
	in := map[string]bool{}
	for _, id := range s.RuleMembers[vipGroup] {
		in[id] = true
	}
	var target string
	for _, id := range res.AuthIDs {
		if !in[id] {
			target = id
			break
		}
	}
	auths, _ := repo.ListAuths(ctx)
	for _, a := range auths {
		if a.ID == target {
			a.Metadata["tags"] = []any{"vip"}
			if _, err := repo.UpsertAuth(ctx, a, "update"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := repo.PollScopeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ = repo.Scope(ctx)
	if len(s.RuleMembers[vipGroup]) != before+1 {
		t.Fatalf("rule members %d -> %d, want +1", before, len(s.RuleMembers[vipGroup]))
	}
}
