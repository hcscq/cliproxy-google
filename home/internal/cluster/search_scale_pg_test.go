package cluster

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Set CLIPROXY_HOME_TEST_POSTGRES_DSN and CLIPROXY_SCALE_N=10000 to run.
func TestCredentialSearchScalePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	n, _ := strconv.Atoi(os.Getenv("CLIPROXY_SCALE_N"))
	if dsn == "" || n == 0 {
		t.Skip("set CLIPROXY_HOME_TEST_POSTGRES_DSN and CLIPROXY_SCALE_N")
	}
	admin, err := gorm.Open(postgres.Open(dsn), databaseGORMConfig(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	schema := "scale_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	admin.Exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := gorm.Open(postgres.Open(postgresDSNWithSearchPath(dsn, schema)), databaseGORMConfig(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := AutoMigrateContext(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	t0 := time.Now()
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("cred-%05d", i)
		a := &coreauth.Auth{ID: ids[i], Index: ids[i], Provider: "antigravity", Label: ids[i] + "@x.com", Status: coreauth.StatusActive,
			Metadata: map[string]any{"email": ids[i] + "@x.com", "type": "antigravity", "project_id": "p-" + ids[i]}}
		if _, err := repo.UpsertAuth(ctx, a, "test"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seeded %d auths in %v", n, time.Since(t0))
	// usage: 20 rows per credential over the last 3 days
	if err := db.Exec(`INSERT INTO usage (timestamp, auth_index, provider, model, failed, created_at, payload)
		SELECT now() - (g % 4320) * interval '1 minute', 'cred-' || lpad((g % ?)::text, 5, '0'), 'antigravity', 'm', false, now(), '{}'::jsonb
		FROM generate_series(1, ?) g`, n, n*20).Error; err != nil {
		t.Fatal(err)
	}
	measure := func(name string, fn func() error) {
		start := time.Now()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-28s %v", name, time.Since(start))
	}
	measure("ListAuths", func() error { l, err := repo.ListAuths(ctx); t.Logf("  %d auths", len(l)); return err })
	measure("LoadCredentialSortStats", func() error {
		s, err := repo.LoadCredentialSortStats(ctx, ids, time.Now().Add(-72*time.Hour))
		t.Logf("  last_used=%d", len(s.LastUsed))
		return err
	})
	measure("LoadCredentialCallStats", func() error { _, err := repo.loadCredentialCallStats(ctx, time.Now().Add(-24*time.Hour)); return err })
	measure("Scope", func() error { _, err := repo.Scope(ctx); return err })
}
