package cluster

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestCredentialGroupMembershipLifecycle(t *testing.T) {
	_, repo := openAPIKeyTestRepository(t)
	ctx := context.Background()
	group, err := repo.CreateChannelGroup(ctx, "default", false)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string) *coreauth.Auth {
		return &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Metadata: map[string]any{"type": "antigravity", "email": id}}
	}
	a := mk("aaaaaaaa-0000-4000-8000-000000000001")
	if _, err := repo.UpsertAuth(ctx, a, "upsert"); err != nil {
		t.Fatal(err)
	}
	// update path must not duplicate membership
	if _, err := repo.UpsertAuth(ctx, a, "upsert"); err != nil {
		t.Fatal(err)
	}
	members, _ := repo.ListChannelGroupDetails(ctx, ChannelGroupDetailFilter{ChannelGroupID: &group.ID})
	if len(members) != 1 || members[0].AuthID != a.ID {
		t.Fatalf("auto-join members = %+v", members)
	}
	if err := repo.SoftDeleteAuth(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	members, _ = repo.ListChannelGroupDetails(ctx, ChannelGroupDetailFilter{ChannelGroupID: &group.ID})
	if len(members) != 0 {
		t.Fatalf("delete should cascade memberships, got %+v", members)
	}

	t.Setenv(DefaultCredentialGroupEnv, "off")
	b := mk("aaaaaaaa-0000-4000-8000-000000000002")
	if _, err := repo.UpsertAuth(ctx, b, "upsert"); err != nil {
		t.Fatal(err)
	}
	members, _ = repo.ListChannelGroupDetails(ctx, ChannelGroupDetailFilter{ChannelGroupID: &group.ID})
	if len(members) != 0 {
		t.Fatalf("auto-join disabled, got %+v", members)
	}
	// orphan purge
	db, _ := repo.database()
	if err := db.Create(&ChannelGroupDetailRecord{ChannelGroupID: group.ID, AuthID: "gone"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ChannelGroupDetailRecord{ChannelGroupID: group.ID, AuthID: b.ID}).Error; err != nil {
		t.Fatal(err)
	}
	n, err := repo.PurgeOrphanChannelGroupDetails(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d %v", n, err)
	}
}

func TestPurgeOrphanRemovesCredentialDependents(t *testing.T) {
	db, repo := openAPIKeyTestRepository(t)
	ctx := context.Background()
	if err := db.Create(&AffinityBindingRecord{BindingKey: "k1", AuthID: "gone-cred", ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PurgeOrphanChannelGroupDetails(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Table("session_affinity").Where("auth_id = ?", "gone-cred").Count(&n)
	if n != 0 {
		t.Fatalf("orphan affinity rows left: %d", n)
	}
}
