package cluster

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRequestLogRetentionDays(t *testing.T) {
	for in, want := range map[int]int{0: 30, 7: 7, -1: 0, 400: 400} {
		if got := RequestLogRetentionDays(in); got != want {
			t.Fatalf("RequestLogRetentionDays(%d)=%d want %d", in, got, want)
		}
	}
}

func openPartitionTestSchema(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), databaseGORMConfig(defaultDatabaseSlowQueryThreshold))
	if err != nil {
		t.Fatal(err)
	}
	schema := "usage_part_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(postgresDSNWithSearchPath(dsn, schema)), databaseGORMConfig(defaultDatabaseSlowQueryThreshold))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, e := db.DB(); e == nil {
			_ = s.Close()
		}
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if s, e := admin.DB(); e == nil {
			_ = s.Close()
		}
	})
	return db
}

func insertTestUsage(t *testing.T, db *gorm.DB, ts time.Time, req string) {
	t.Helper()
	rec := UsageRecord{Timestamp: ts, RequestID: req, Provider: "antigravity", Model: "m", PayloadJSON: JSONB(`{}`), CreatedAt: ts}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatalf("insert usage: %v", err)
	}
}

func relkind(t *testing.T, db *gorm.DB) string {
	var k string
	db.Raw(`SELECT c.relkind::text FROM pg_class c WHERE c.relname='usage' AND c.relnamespace = to_regnamespace(current_schema())`).Scan(&k)
	return k
}

// Existing plain table with history -> partitioned, rows preserved, ids
// continue, indexes recreated on the parent, legacy copy kept.
func TestConvertUsageToPartitionedPostgres(t *testing.T) {
	db := openPartitionTestSchema(t)
	ctx := context.Background()
	if err := db.AutoMigrate(&UsageRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertTestUsage(t, db, now, "today")
	insertTestUsage(t, db, now.AddDate(0, 0, -2), "two-days")
	insertTestUsage(t, db, now.AddDate(0, 0, -90), "old")
	var maxID int64
	db.Raw(`SELECT max(id) FROM "usage"`).Scan(&maxID)

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := convertUsageToPartitioned(ctx, tx, 30); err != nil {
			return err
		}
		return tx.AutoMigrate(&UsageRecord{})
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if k := relkind(t, db); k != "p" {
		t.Fatalf("relkind=%q want p", k)
	}
	var n, legacy int64
	db.Raw(`SELECT count(*) FROM "usage"`).Scan(&n)
	db.Raw(`SELECT count(*) FROM usage_legacy`).Scan(&legacy)
	if n != 3 || legacy != 3 {
		t.Fatalf("rows=%d legacy=%d want 3/3", n, legacy)
	}
	var inDefault int64
	db.Raw(`SELECT count(*) FROM usage_p_default`).Scan(&inDefault)
	if inDefault != 1 {
		t.Fatalf("default partition rows=%d want 1 (90-day-old row)", inDefault)
	}
	insertTestUsage(t, db, now, "after")
	var newID int64
	db.Raw(`SELECT id FROM "usage" WHERE request_id='after'`).Scan(&newID)
	if newID <= maxID {
		t.Fatalf("id sequence not continued: %d <= %d", newID, maxID)
	}
	var idx int64
	db.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='usage' AND indexname='idx_usage_request_id'`).Scan(&idx)
	if idx != 1 {
		t.Fatalf("parent index idx_usage_request_id missing")
	}
	// Converting again is a no-op.
	if err := db.Transaction(func(tx *gorm.DB) error { return convertUsageToPartitioned(ctx, tx, 30) }); err != nil {
		t.Fatalf("second convert: %v", err)
	}

	repo := NewRepository(db)
	// 30-day retention: removes only the 90-day-old row (default partition).
	if err := repo.MaintainUsageRetention(ctx, 30); err != nil {
		t.Fatal(err)
	}
	db.Raw(`SELECT count(*) FROM "usage"`).Scan(&n)
	if n != 3 {
		t.Fatalf("after 30d retention rows=%d want 3", n)
	}
	// 1-day retention: drops the partition holding the two-day-old row.
	if err := repo.MaintainUsageRetention(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var twoDays int64
	db.Raw(`SELECT count(*) FROM "usage" WHERE request_id='two-days'`).Scan(&twoDays)
	if twoDays != 0 {
		t.Fatalf("two-day-old row should be dropped with 1-day retention")
	}
	status, err := repo.UsageRetentionStatus(ctx, 1)
	if err != nil || !status.Partitioned || !status.LegacyTable || len(status.Partitions) == 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(status.LastDropped) == 0 {
		t.Fatalf("expected a dropped partition in status: %+v", status)
	}
}

// A row that landed in the default partition is moved when its day's
// partition is created.
func TestUsageStrayRowsMovedFromDefaultPostgres(t *testing.T) {
	db := openPartitionTestSchema(t)
	ctx := context.Background()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := convertUsageToPartitioned(ctx, tx, 30); err != nil { // fresh path
			return err
		}
		return tx.AutoMigrate(&UsageRecord{})
	})
	if err != nil {
		t.Fatal(err)
	}
	future := utcDay(time.Now()).AddDate(0, 0, usagePartitionsAhead+5).Add(time.Hour)
	insertTestUsage(t, db, future, "future")
	if err := db.Transaction(func(tx *gorm.DB) error { return ensureUsageDayPartition(tx, utcDay(future)) }); err != nil {
		t.Fatal(err)
	}
	var inDefault, inDay int64
	db.Raw(`SELECT count(*) FROM usage_p_default`).Scan(&inDefault)
	db.Raw(`SELECT count(*) FROM ` + usagePartitionName(future)).Scan(&inDay)
	if inDefault != 0 || inDay != 1 {
		t.Fatalf("default=%d day=%d want 0/1", inDefault, inDay)
	}
}

func TestUsageRetentionSQLite(t *testing.T) {
	db, err := OpenSQLite(context.Background(), t.TempDir()+"/h.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&UsageRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertTestUsage(t, db, now, "new")
	insertTestUsage(t, db, now.AddDate(0, 0, -40), "old")
	repo := NewRepository(db)
	if err := repo.MaintainUsageRetention(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&UsageRecord{}).Count(&n)
	if n != 1 {
		t.Fatalf("rows=%d want 1", n)
	}
}
