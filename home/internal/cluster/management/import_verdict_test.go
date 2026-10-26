package management

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestImportVerdictIsImmutableAndSticky(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	ctx := context.Background()
	if err := db.AutoMigrate(&cluster.CredentialHealthCheck{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = repo.SaveHealthCheck(ctx, cluster.CredentialHealthCheck{AuthUUID: "a1", Status: "running", BatchID: "b", CheckedAt: now})
	if err := repo.SaveHealthCheck(ctx, cluster.CredentialHealthCheck{AuthUUID: "a1", Status: "fail", Categories: "quota_used", Reasons: "额度非新：最低剩余 60%", Tier: "g1-pro-tier", BatchID: "b", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = repo.SaveHealthCheck(ctx, cluster.CredentialHealthCheck{AuthUUID: "a1", Status: "pass", BatchID: "b", CheckedAt: now.Add(10 * time.Minute)})
	_ = repo.SaveHealthCheck(ctx, cluster.CredentialHealthCheck{AuthUUID: "manual", Status: "fail", CheckedAt: now})
	v, _ := repo.ListImportVerdicts(ctx)
	if len(v) != 1 || v["a1"].Status != "fail" || !v["a1"].Sticky || v["a1"].CategoryList()[0] != "quota_used" {
		t.Fatalf("verdicts: %+v", v)
	}
	if n, err := repo.ClearImportVerdictSticky(ctx, []string{"a1"}); err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	v, _ = repo.ListImportVerdicts(ctx)
	if v["a1"].Sticky || v["a1"].ClearedAt == nil || v["a1"].Status != "fail" {
		t.Fatalf("after clear: %+v", v["a1"])
	}
}
