package cluster

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestBulkMembershipAndKeyScopes(t *testing.T) {
	repo := newScopeTestRepo(t)
	ctx := context.Background()
	res, err := repo.SeedSynthetic(ctx, SyntheticScale{Credentials: 20, Models: 10, Keys: 5, ChannelGroups: 2, ModelGroups: 1, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := res.Channels[1]
	s, _ := repo.Scope(ctx)
	before := len(s.StaticMembers[g])
	r1, err := repo.BulkChannelGroupMembers(ctx, g, res.AuthIDs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Added != len(res.AuthIDs)-before || r1.Unchanged != before {
		t.Fatalf("unexpected %+v (before %d)", r1, before)
	}
	r2, err := repo.BulkChannelGroupMembers(ctx, g, nil, res.AuthIDs[:5])
	if err != nil || r2.Removed != 5 {
		t.Fatalf("remove %+v %v", r2, err)
	}
	s, _ = repo.Scope(ctx)
	if len(s.StaticMembers[g]) != len(res.AuthIDs)-5 {
		t.Fatalf("members %d", len(s.StaticMembers[g]))
	}
	if _, err := repo.BulkChannelGroupMembers(ctx, 9999, []string{"x"}, nil); err == nil {
		t.Fatal("missing group must fail")
	}
	m, err := repo.BulkModelGroupMembers(ctx, res.MGroups[0], []string{"brand-new-model", res.Models[0]}, nil, []uint{g})
	if err != nil || m.Added < 1 {
		t.Fatalf("model bulk %+v %v", m, err)
	}
	var ids []uint
	repo.db.Model(&APIKeyRecord{}).Pluck("id", &ids)
	k, err := repo.BulkUpdateAPIKeyScopes(ctx, KeyScopeChange{IDs: ids, ClearChannels: true, AddChannels: []uint{g}, ClearModelGroups: true})
	if err != nil || k.Added+k.Unchanged != len(ids) {
		t.Fatalf("keys %+v %v", k, err)
	}
	got, err := repo.CachedAllowedAuthIDsForAPIKey(ctx, res.APIKeys[0])
	if err != nil || len(got) != len(res.AuthIDs)-5 {
		t.Fatalf("scope after bulk: %d %v", len(got), err)
	}
}

func TestImportGroupsMergeReplaceDryRun(t *testing.T) {
	repo := newScopeTestRepo(t)
	ctx := context.Background()
	if _, err := repo.SeedSynthetic(ctx, SyntheticScale{Credentials: 10, Models: 3, Keys: 1, ChannelGroups: 0, ModelGroups: 0, Seed: 2}); err != nil {
		t.Fatal(err)
	}
	resolve := func(s string) (string, bool) {
		if strings.HasPrefix(s, "synthetic-") {
			return s, true
		}
		return "", false
	}
	doc := GroupExport{CredentialGroups: []CredentialGroupExport{{Name: "imp", Enabled: true, Members: []string{"synthetic-0001", "synthetic-0002", "ghost@x"}}},
		ModelGroups: []ModelGroupExport{{Name: "mimp", Enabled: true, Models: []ModelEntryExport{{Model: "m1", CredentialGroups: []string{"imp"}}}}}}
	rep, err := repo.ImportGroups(ctx, doc, resolve, false, true)
	if err != nil || rep.GroupsCreated != 2 || len(rep.Unresolved) != 1 {
		t.Fatalf("dry run %+v %v", rep, err)
	}
	var n int64
	repo.db.Model(&ChannelGroupRecord{}).Count(&n)
	if n != 0 {
		t.Fatal("dry run must not persist")
	}
	if _, err := repo.ImportGroups(ctx, doc, resolve, false, false); err != nil {
		t.Fatal(err)
	}
	doc.CredentialGroups[0].Members = []string{"synthetic-0003"}
	rep, err = repo.ImportGroups(ctx, doc, resolve, true, false)
	if err != nil || rep.MembersAdded != 1 || rep.MembersRemoved != 2 {
		t.Fatalf("replace %+v %v", rep, err)
	}
	exp, err := repo.ExportGroups(ctx)
	if err != nil || len(exp.CredentialGroups) != 1 || len(exp.CredentialGroups[0].Members) != 1 || exp.ModelGroups[0].Models[0].CredentialGroups[0] != "imp" {
		t.Fatalf("export %+v %v", exp, err)
	}
}

func TestGroupRuleMatching(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	two := 2
	r := GroupRule{EmailPattern: "*@corp.com", TagsAny: []string{"VIP"}, MinPriority: &two}
	a := testAuth("a", "antigravity", "x@corp.com", []any{"vip"}, 3)
	b := testAuth("b", "antigravity", "x@other.com", []any{"vip"}, 3)
	c := testAuth("c", "antigravity", "y@corp.com", []any{"vip"}, 1)
	if !r.Matches(a) || r.Matches(b) || r.Matches(c) {
		t.Fatal("rule mismatch")
	}
	if (GroupRule{}).Matches(a) {
		t.Fatal("empty rule must match nothing")
	}
	if !(GroupRule{MatchAll: true}).Matches(a) {
		t.Fatal("match_all")
	}
	if err := (GroupRule{EmailPattern: "["}).Validate(); err == nil {
		t.Fatal("bad glob must fail")
	}
}

func testAuth(id, provider, email string, tags []any, prio int) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: provider, Label: email, Metadata: map[string]any{"email": email, "tags": tags, "priority": float64(prio)}}
}
