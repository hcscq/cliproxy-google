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

// Free-tier credentials never count towards the reserve minimum (Postgres).
func TestCountActiveCredentialsExcludesFreeTierPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("DROP SCHEMA public CASCADE")
	db.Exec("CREATE SCHEMA public")
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	rows := []struct {
		id, tier string
		cooling  bool
	}{
		{"pro-1", "g1-pro-tier", false}, {"pro-2", "g1-pro-tier", true}, {"ultra", "g1-ultra-tier", false},
		{"free-1", "free-tier", false}, {"free-2", "FREE-tier", true}, {"untiered", "", false},
	}
	for _, r := range rows {
		meta := `{"metadata":{"refresh_token":"x","subscription_tier":"` + r.tier + `"}}`
		var retry any
		if r.cooling {
			retry = time.Now().Add(time.Hour)
		}
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, status, disabled, unavailable, next_retry_after, created_at) VALUES (?, ?::jsonb, ?, 'active', false, ?, ?, now())`,
			r.id, meta, r.id, r.cooling, retry).Error; err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := repo.CountActiveCredentials(ctx, true); n != 4 {
		t.Fatalf("enabled = %d, want 4 (pro-1, pro-2, ultra, untiered)", n)
	}
	if n, _ := repo.CountActiveCredentials(ctx, false); n != 3 {
		t.Fatalf("usable = %d, want 3", n)
	}
}

// New credentials from any path (here: no import row, not reserved, e.g. OAuth
// login) are picked up by the health sweep; checked ones are not.
func TestHealthSweepIncludesNewNonImportedCredentialsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("DROP SCHEMA public CASCADE")
	db.Exec("CREATE SCHEMA public")
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	ins := func(id string, created time.Time) {
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, provider, status, disabled, created_at) VALUES (?, '{}'::jsonb, ?, 'antigravity', 'active', false, ?)`, id, id, created).Error; err != nil {
			t.Fatal(err)
		}
	}
	ins("oauth-new", time.Now().Add(-time.Minute))
	ins("old", time.Now().Add(-72*time.Hour))
	ins("checked-new", time.Now().Add(-time.Minute))
	if err := repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "checked-new", Status: "fail", Categories: "needs_verify", CheckedAt: time.Now(), VerifyURL: "https://accounts.google.com/x"}); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.ListHealthSweepIDs(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != "oauth-new" {
		t.Fatalf("sweep = %v err=%v", ids, err)
	}
	all, _ := repo.ListHealthChecks(ctx)
	if all["checked-new"].VerifyURL != "https://accounts.google.com/x" {
		t.Fatalf("verify_url not stored: %+v", all["checked-new"])
	}
}
