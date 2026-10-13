package cluster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCallStatPartitionConversionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{`DROP TABLE IF EXISTS credential_call_stat CASCADE`, `DROP TABLE IF EXISTS credential_model_stat CASCADE`,
		`CREATE TABLE "credential_call_stat" ("auth_index" text NOT NULL, "bucket" timestamptz NOT NULL, "class" text NOT NULL, "calls" bigint NOT NULL, PRIMARY KEY ("auth_index","bucket","class"))`,
		`CREATE INDEX "idx_credential_call_stat_bucket" ON "credential_call_stat" ("bucket")`} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Hour)
	old := now.Add(-30 * 24 * time.Hour)
	db.Exec(`INSERT INTO credential_call_stat VALUES ('a', ?, 'ok', 3), ('b', ?, 'ok', 1), ('a', ?, 'ok', 9)`, now, now.Add(-24*time.Hour), old)
	if err := ensureCredentialCallStatTables(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialCallStatTables(ctx, db); err != nil { // idempotent
		t.Fatal(err)
	}
	kind, _ := tableRelkind(db, "credential_call_stat")
	if kind != "p" {
		t.Fatalf("relkind=%q", kind)
	}
	var n int64
	db.Raw(`SELECT count(*) FROM credential_call_stat`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows=%d want 2 (expired row dropped)", n)
	}
	// upsert used by the rollup must work on the partitioned table
	if err := db.Exec(`INSERT INTO credential_call_stat VALUES ('a', ?, 'ok', 2) ON CONFLICT (auth_index, bucket, class) DO UPDATE SET calls = credential_call_stat.calls + EXCLUDED.calls`, now).Error; err != nil {
		t.Fatal(err)
	}
	var calls int64
	db.Raw(`SELECT calls FROM credential_call_stat WHERE auth_index='a' AND bucket=?`, now).Scan(&calls)
	if calls != 5 {
		t.Fatalf("calls=%d", calls)
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS credential_call_stat_p20000101 PARTITION OF credential_call_stat FOR VALUES FROM ('2000-01-01') TO ('2000-01-02')`)
	r := &Repository{db: db}
	if err := r.PruneCredentialCallStats(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	db.Raw(`SELECT to_regclass('credential_call_stat_p20000101') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("expired partition not dropped")
	}
}
