package cluster

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Homes starting together must not fail their migration racing on the
// late-added usage columns.
func TestEnsureUsageColumnsConcurrentPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db.Exec(`DROP TABLE IF EXISTS "usage" CASCADE`)
	if err := db.Exec(`CREATE TABLE "usage" (id bigserial primary key, "timestamp" timestamptz)`).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP TABLE IF EXISTS "usage" CASCADE`)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureUsageFailBodyColumns(ctx, db)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure failed: %v", err)
		}
	}
	if !db.Migrator().HasColumn(&UsageRecord{}, "StreamID") {
		t.Fatal("stream_id missing")
	}
}
