package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Through a transaction-mode pgbouncer with prepared statement support, an
// ADD COLUMN must not break repeated "SELECT *" (cached plan result type).
func TestPoolerSchemaChangeNoCachedPlanFailure(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	pooler := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_PGBOUNCER_ADDR"))
	if dsn == "" || pooler == "" {
		t.Skip("needs CLIPROXY_HOME_TEST_POSTGRES_DSN and CLIPROXY_HOME_TEST_PGBOUNCER_ADDR")
	}
	t.Setenv("HOME_PG_POOLER_ADDR", pooler)
	db, err := Open(context.Background(), PGSQLConfig{PostgresURI: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if !DatabasePoolStatus(context.Background(), db).ViaPooler {
		t.Fatal("not via pooler")
	}
	db.Exec(`DROP TABLE IF EXISTS shape_t`)
	if err := db.Exec(`CREATE TABLE shape_t (id int)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO shape_t VALUES (1)`)
	read := func() error {
		var rows []map[string]any
		return db.Raw(`SELECT * FROM shape_t WHERE id = ?`, 1).Scan(&rows).Error
	}
	for i := 0; i < 20; i++ { // warm every pooled server connection
		if err := read(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`ALTER TABLE shape_t ADD COLUMN extra bigint`).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := read(); err != nil {
			t.Fatal(fmt.Errorf("after ADD COLUMN, read %d: %w", i, err))
		}
	}
}

func TestDSNWithExecMode(t *testing.T) {
	if got := dsnWithExecMode("postgres://u:p@h:5432/d?sslmode=disable"); !strings.Contains(got, "default_query_exec_mode=simple_protocol") || !strings.Contains(got, "sslmode=disable") {
		t.Fatal(got)
	}
	if got := dsnWithExecMode("host=h dbname=d"); got != "host=h dbname=d default_query_exec_mode=simple_protocol" {
		t.Fatal(got)
	}
	if got := dsnWithExecMode("host=h default_query_exec_mode=exec"); got != "host=h default_query_exec_mode=exec" {
		t.Fatal(got)
	}
}
