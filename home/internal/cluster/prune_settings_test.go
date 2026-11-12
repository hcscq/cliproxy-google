package cluster

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func overridePruneSettings(t *testing.T, s pruneSettings) {
	t.Helper()
	orig := loadPruneSettings
	loadPruneSettings = func() pruneSettings { return s }
	t.Cleanup(func() { loadPruneSettings = orig })
}

func TestLoadPruneSettingsFromEnv(t *testing.T) {
	t.Setenv(envPruneBatchSize, "1000")
	t.Setenv(envPruneBatchSleep, "200ms")
	t.Setenv(envPruneMaxDuration, "30s")
	t.Setenv(envPruneMaxRows, "50000")
	got := loadPruneSettingsFromEnv()
	if got.BatchSize != 1000 || got.BatchSleep != 200*time.Millisecond || got.MaxDuration != 30*time.Second || got.MaxRows != 50000 {
		t.Fatalf("explicit defaults: %+v", got)
	}

	t.Setenv(envPruneBatchSize, "250")
	t.Setenv(envPruneBatchSleep, "50ms")
	t.Setenv(envPruneMaxDuration, "15s")
	t.Setenv(envPruneMaxRows, "3000")
	t.Setenv(envPruneStartupJitter, "10s")
	got = loadPruneSettingsFromEnv()
	if got.BatchSize != 250 || got.BatchSleep != 50*time.Millisecond || got.MaxDuration != 15*time.Second || got.MaxRows != 3000 {
		t.Fatalf("overrides: %+v", got)
	}
	if got.StartupJitterMin != 0 || got.StartupJitterMax != 10*time.Second {
		t.Fatalf("jitter: min=%s max=%s", got.StartupJitterMin, got.StartupJitterMax)
	}

	t.Setenv(envPruneBatchSize, "0") // clamped to 1
	t.Setenv(envPruneStartupJitter, "0s")
	got = loadPruneSettingsFromEnv()
	if got.BatchSize != 1 {
		t.Fatalf("batch size 0 should clamp to 1, got %d", got.BatchSize)
	}
	if got.StartupJitterMax != 0 {
		t.Fatalf("zero jitter should disable, got %s", got.StartupJitterMax)
	}
}

func TestDefaultPruneSettingsMatchApprovedValues(t *testing.T) {
	s := defaultPruneSettings()
	if s.BatchSize != 1000 || s.BatchSleep != 200*time.Millisecond || s.MaxDuration != 30*time.Second || s.MaxRows != 50000 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.StartupJitterMin != 5*time.Second || s.StartupJitterMax != 45*time.Second {
		t.Fatalf("unexpected jitter defaults: min=%s max=%s", s.StartupJitterMin, s.StartupJitterMax)
	}
}

func TestPickPruneStartupJitter(t *testing.T) {
	if d := pickPruneStartupJitter(pruneSettings{}); d != 0 {
		t.Fatalf("empty settings jitter = %s", d)
	}
	fixed := pickPruneStartupJitter(pruneSettings{StartupJitterMin: 2 * time.Second, StartupJitterMax: 2 * time.Second})
	if fixed != 2*time.Second {
		t.Fatalf("fixed jitter = %s", fixed)
	}
	for i := 0; i < 20; i++ {
		d := pickPruneStartupJitter(defaultPruneSettings())
		if d < 5*time.Second || d > 45*time.Second {
			t.Fatalf("default jitter out of range: %s", d)
		}
	}
}

func TestPruneBudgetExhausted(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	s := pruneSettings{MaxRows: 10, MaxDuration: 30 * time.Second}
	if pruneBudgetExhausted(time.Now(), 0, s) {
		t.Fatal("empty run should not be exhausted")
	}
	if !pruneBudgetExhausted(time.Now(), 10, s) {
		t.Fatal("max rows should exhaust")
	}
	if !pruneBudgetExhausted(start, 1, s) {
		t.Fatal("max duration should exhaust")
	}
}

func TestPruneAppLogsSkipsWhenNotLeader(t *testing.T) {
	repo := newRefreshTestRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := AppLogRecord{Line: "old", Level: "info", Timestamp: now.AddDate(0, 0, -5), CreatedAt: now.AddDate(0, 0, -5)}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	repo.SetMaintenanceLeader(func() bool { return false })
	n, err := repo.PruneAppLogs(context.Background(), 3)
	if err != nil || n != 0 {
		t.Fatalf("skip = %d, %v; want 0, nil", n, err)
	}
	var left int64
	db.Model(&AppLogRecord{}).Count(&left)
	if left != 1 {
		t.Fatalf("rows left = %d, want 1", left)
	}
}

func TestPruneAppLogsRespectsMaxRows(t *testing.T) {
	repo := newRefreshTestRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var rows []AppLogRecord
	for i := 0; i < 5; i++ {
		ts := now.AddDate(0, 0, -5)
		rows = append(rows, AppLogRecord{Line: "old", Level: "info", Timestamp: ts, CreatedAt: ts})
	}
	rows = append(rows, AppLogRecord{Line: "new", Level: "info", Timestamp: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour)})
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	overridePruneSettings(t, pruneSettings{BatchSize: 1, MaxRows: 2, BatchSleep: 0})
	n, err := repo.PruneAppLogs(context.Background(), 3)
	if err != nil || n != 2 {
		t.Fatalf("PruneAppLogs = %d, %v; want 2", n, err)
	}
	var left int64
	db.Model(&AppLogRecord{}).Count(&left)
	if left != 4 { // 3 old leftover + 1 recent
		t.Fatalf("rows left = %d, want 4", left)
	}
}

func TestPruneBillingChargesSkipsWhenNotLeader(t *testing.T) {
	repo := newRefreshTestRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(testBillingCharge(t, 1, time.Now().AddDate(0, 0, -10))).Error; err != nil {
		t.Fatal(err)
	}
	repo.SetMaintenanceLeader(func() bool { return false })
	n, err := repo.PruneBillingCharges(context.Background(), 7)
	if err != nil || n != 0 {
		t.Fatalf("skip = %d, %v; want 0, nil", n, err)
	}
	var left int64
	db.Model(&BillingChargeRecord{}).Count(&left)
	if left != 1 {
		t.Fatalf("rows left = %d, want 1", left)
	}
}

func TestPruneBillingChargesRespectsMaxRows(t *testing.T) {
	repo := newRefreshTestRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	for i := 1; i <= 5; i++ {
		if err := db.Create(testBillingCharge(t, uint(i), old)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(testBillingCharge(t, 99, time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatal(err)
	}
	overridePruneSettings(t, pruneSettings{BatchSize: 1, MaxRows: 2, BatchSleep: 0})
	n, err := repo.PruneBillingCharges(context.Background(), 7)
	if err != nil || n != 2 {
		t.Fatalf("PruneBillingCharges = %d, %v; want 2", n, err)
	}
	var left int64
	db.Model(&BillingChargeRecord{}).Count(&left)
	if left != 4 {
		t.Fatalf("rows left = %d, want 4", left)
	}
}

func testBillingCharge(t *testing.T, usageID uint, created time.Time) *BillingChargeRecord {
	t.Helper()
	id := "chg-" + time.Now().Format("150405.000000000") + "-" + strconv.Itoa(int(usageID))
	return &BillingChargeRecord{
		ID:            id,
		UsageID:       usageID,
		PayloadHash:   "hash-" + id,
		Provider:      "antigravity",
		Model:         "gemini-test",
		PriceSnapshot: JSONB("{}"),
		CreatedAt:     created,
	}
}

func TestWaitForMaintenanceLeader(t *testing.T) {
	repo := newRefreshTestRepository(t)
	if !waitForMaintenanceLeader(context.Background(), repo) {
		t.Fatal("nil leader func should be treated as leader")
	}
	ready := make(chan struct{})
	repo.SetMaintenanceLeader(func() bool {
		select {
		case <-ready:
			return true
		default:
			return false
		}
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(ready)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !waitForMaintenanceLeader(ctx, repo) {
		t.Fatal("should become leader after brief wait")
	}

	repo.SetMaintenanceLeader(func() bool { return false })
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if waitForMaintenanceLeader(ctx2, repo) {
		t.Fatal("cancelled wait should not report leader")
	}
}
