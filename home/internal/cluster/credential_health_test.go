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

func TestHealthSweepIDs(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, _ := repo.database()
	for i, id := range []string{"r1", "r2", "n1", "old"} {
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, provider, status, disabled, seq, created_at) VALUES (?, '{}', ?, 'antigravity', 'active', false, ?, ?)`, id, id, i+1, time.Now()).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, _ = repo.SetCredentialsReserved(ctx, []string{"r1", "r2"}, true, "reserve", "")
	_ = repo.RecordCredentialImports(ctx, []string{"n1"}, "b", time.Now(), "rt")
	_ = repo.RecordCredentialImports(ctx, []string{"old"}, "b0", time.Now().Add(-48*time.Hour), "rt")
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r2", Status: "pass", CheckedAt: time.Now()})
	ids, err := repo.ListHealthSweepIDs(ctx, 10)
	if err != nil || len(ids) != 2 || ids[0] != "r1" || ids[1] != "n1" {
		t.Fatalf("sweep = %v err=%v", ids, err)
	}
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r1", Status: "running", CheckedAt: time.Now()})
	if ids, _ := repo.ListHealthSweepIDs(ctx, 10); len(ids) != 1 {
		t.Fatalf("running must not be re-picked: %v", ids)
	}
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r1", Status: "running", CheckedAt: time.Now().Add(-time.Hour)})
	if ids, _ := repo.ListHealthSweepIDs(ctx, 10); len(ids) != 2 {
		t.Fatalf("stale running must be re-picked: %v", ids)
	}
}

func TestRegionScanIDs(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, _ := repo.database()
	for i, id := range []string{"act", "res"} {
		_ = db.Exec(`INSERT INTO auth (uuid, auth_json, id, provider, status, disabled, seq, created_at) VALUES (?, '{}', ?, 'antigravity', 'active', false, ?, ?)`, id, id, i+1, time.Now()).Error
	}
	_, _ = repo.SetCredentialsReserved(ctx, []string{"res"}, true, "reserve", "")
	ids, _ := repo.ListRegionScanIDs(ctx, 10)
	if len(ids) != 1 || ids[0] != "act" {
		t.Fatalf("scan = %v (active only)", ids)
	}
	_ = repo.SaveRegionCheck(ctx, CredentialRegionCheck{AuthUUID: "act", Codes: "UNSUPPORTED_LOCATION", CheckedAt: time.Now()})
	if ids, _ := repo.ListRegionScanIDs(ctx, 10); len(ids) != 0 {
		t.Fatalf("recently scanned must be skipped: %v", ids)
	}
}
