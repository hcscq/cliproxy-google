package cluster

import (
	"sync"
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
	for i, id := range []string{"r1", "r2", "n1", "aged", "old"} {
		created := time.Now()
		if id == "old" {
			created = created.Add(-72 * time.Hour) // new credentials (any path) are swept for 24h
		}
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, provider, status, disabled, seq, created_at) VALUES (?, '{}', ?, 'antigravity', 'active', false, ?, ?)`, id, id, i+1, created).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, _ = repo.SetCredentialsReserved(ctx, []string{"r1", "r2"}, true, "reserve", "")
	_ = repo.RecordCredentialImports(ctx, []string{"n1"}, "b", time.Now(), "rt")
	_ = repo.RecordCredentialImports(ctx, []string{"aged"}, "b1", time.Now().Add(-healthSweepImportGrace-time.Minute), "rt")
	_ = repo.RecordCredentialImports(ctx, []string{"old"}, "b0", time.Now().Add(-48*time.Hour), "rt")
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r2", Status: "pass", CheckedAt: time.Now()})
	ids, err := repo.ListHealthSweepIDs(ctx, 10)
	// n1 is inside the import grace window and is left to afterImport; aged is past grace and still unchecked.
	if err != nil || len(ids) != 2 || ids[0] != "r1" || ids[1] != "aged" {
		t.Fatalf("sweep = %v err=%v", ids, err)
	}
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r1", Status: "running", CheckedAt: time.Now()})
	if ids, _ := repo.ListHealthSweepIDs(ctx, 10); len(ids) != 1 || ids[0] != "aged" {
		t.Fatalf("running must not be re-picked: %v", ids)
	}
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "r1", Status: "running", CheckedAt: time.Now().Add(-time.Hour)})
	if ids, _ := repo.ListHealthSweepIDs(ctx, 10); len(ids) != 2 {
		t.Fatalf("stale running must be re-picked: %v", ids)
	}
}

func TestSaveHealthCheckPreservesBatchAttribution(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	now := time.Now().UTC()
	imported := now.Add(-time.Minute)
	batch := "20261112-2235-7da7061c"
	if err := repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a1", Status: "pass", BatchID: batch, ImportedAt: &imported, CheckedAt: now, Probe: "import"}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(11 * time.Millisecond)
	if err := repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a1", Status: "pass", BatchID: "", ImportedAt: nil, CheckedAt: later, Probe: "sweep", Reasons: "sweep-result"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.GetHealthCheck(ctx, "a1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.BatchID != batch {
		t.Fatalf("empty sweep write cleared batch_id: %+v", got)
	}
	if got.ImportedAt == nil || !got.ImportedAt.Equal(imported) {
		t.Fatalf("empty sweep write cleared imported_at: %+v", got)
	}
	if got.Probe != "sweep" || got.Reasons != "sweep-result" || got.Status != "pass" {
		t.Fatalf("health result itself must update: %+v", got)
	}

	// Incoming empty, no health row yet: fill ownership from credential_import.
	_ = repo.RecordCredentialImports(ctx, []string{"a2"}, "batch-x", imported, "rt")
	if err := repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a2", Status: "pass", CheckedAt: now, Probe: "sweep-first"}); err != nil {
		t.Fatal(err)
	}
	got, _, err = repo.GetHealthCheck(ctx, "a2")
	if err != nil || got.BatchID != "batch-x" {
		t.Fatalf("should attach batch from import: %+v err=%v", got, err)
	}
	if got.ImportedAt == nil || !got.ImportedAt.Equal(imported) {
		t.Fatalf("should attach imported_at from import: %+v", got)
	}

	// Out-of-order / concurrent: whichever write lands last, batch_id stays.
	_ = repo.RecordCredentialImports(ctx, []string{"a3"}, "batch-y", imported, "rt")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a3", Status: "pass", BatchID: "batch-y", ImportedAt: &imported, CheckedAt: now, Probe: "import"})
	}()
	go func() {
		defer wg.Done()
		errs <- repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a3", Status: "pass", CheckedAt: now.Add(time.Millisecond), Probe: "sweep"})
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _, err = repo.GetHealthCheck(ctx, "a3")
	if err != nil || got.BatchID != "batch-y" {
		t.Fatalf("concurrent empty write must not clear batch_id: %+v err=%v", got, err)
	}
}

func TestHealthSweepSkipsFreshImport(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, _ := repo.database()
	now := time.Now()
	for i, id := range []string{"fresh", "inflight", "reserve"} {
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, provider, status, disabled, seq, created_at) VALUES (?, '{}', ?, 'antigravity', 'active', false, ?, ?)`, id, id, i+1, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, _ = repo.SetCredentialsReserved(ctx, []string{"reserve"}, true, "reserve", "")
	_ = repo.RecordCredentialImports(ctx, []string{"fresh", "inflight"}, "20261112-2235-aabbccdd", now, "rt")
	impAt := now
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "inflight", Status: "running", BatchID: "20261112-2235-aabbccdd", ImportedAt: &impAt, CheckedAt: now})

	ids, err := repo.ListHealthSweepIDs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == "fresh" || id == "inflight" {
			t.Fatalf("fresh import must not be swept (would start a second ImportCheck): %v", ids)
		}
	}
	if len(ids) != 1 || ids[0] != "reserve" {
		t.Fatalf("only the reserved unchecked credential should be swept: %v", ids)
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
