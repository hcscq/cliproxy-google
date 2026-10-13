package cluster

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func checkAuthSeq(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO auth (uuid, auth_json, created_at) VALUES ('c', '{}', '2026-01-03'), ('a', '{}', '2026-01-01'), ('b', '{}', '2026-01-02')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := ensureAuthSeq(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	get := func(u string) int64 {
		var n int64
		if err := db.Raw(`SELECT seq FROM auth WHERE uuid = ?`, u).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if get("a") != 1 || get("b") != 2 || get("c") != 3 {
		t.Fatalf("backfill a=%d b=%d c=%d", get("a"), get("b"), get("c"))
	}
	// soft-deleted row keeps its number; new row never reuses it
	db.Exec(`UPDATE auth SET deleted_at = CURRENT_TIMESTAMP WHERE uuid = 'c'`)
	if err := db.Exec(`INSERT INTO auth (uuid, auth_json, created_at) VALUES ('d', '{}', '2026-01-04')`).Error; err != nil {
		t.Fatal(err)
	}
	if get("d") != 4 {
		t.Fatalf("new seq %d", get("d"))
	}
	r := &Repository{db: db}
	m, err := r.AuthSeqs(ctx)
	if err != nil || m["d"] != 4 || m["a"] != 1 {
		t.Fatalf("AuthSeqs %v %v", m, err)
	}
	if _, ok := m["c"]; ok {
		t.Fatal("deleted row listed")
	}
}

func TestAuthSeqSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:authseq?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&AuthRecord{}); err != nil {
		t.Fatal(err)
	}
	checkAuthSeq(t, db)
}

func TestAuthSeqPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`DROP TABLE IF EXISTS auth CASCADE`)
	db.Exec(`DROP SEQUENCE IF EXISTS auth_seq_seq`)
	if err := db.AutoMigrate(&AuthRecord{}); err != nil {
		t.Fatal(err)
	}
	checkAuthSeq(t, db)
}
