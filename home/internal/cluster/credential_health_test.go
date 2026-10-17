package cluster

import (
	"testing"
	"time"
)

func TestCredentialHealthStore(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	now := time.Now()
	if err := repo.RecordCredentialImports(ctx, []string{"a1", "a2"}, "b1", now, "refresh_token"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordCredentialImports(ctx, []string{"a1"}, "b2", now, "x"); err != nil {
		t.Fatal(err)
	}
	imps, _ := repo.ListCredentialImports(ctx)
	if len(imps) != 2 || imps["a1"].BatchID != "b1" {
		t.Fatalf("imports = %+v (first batch must win)", imps)
	}
	if err := repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a1", Status: "fail", Categories: "needs_verify,tier", Reasons: "a\nb", Evidence: "raw", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a2", Status: "pass", CheckedAt: now})
	all, _ := repo.ListHealthChecks(ctx)
	if len(all) != 2 || len(all["a1"].CategoryList()) != 2 || len(all["a1"].ReasonList()) != 2 || len(all["a2"].CategoryList()) != 0 {
		t.Fatalf("checks = %+v", all)
	}
	failed, _ := repo.ListHealthFailedIDs(ctx)
	if _, ok := failed["a1"]; !ok || len(failed) != 1 {
		t.Fatalf("failed = %v", failed)
	}
}
