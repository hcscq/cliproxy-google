package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

type shapeRecord struct {
	ID   int    `gorm:"column:id;primaryKey"`
	Name string `gorm:"column:name"`
}

func (shapeRecord) TableName() string { return "shape_t" }

// Through a transaction-mode pgbouncer with prepared statements, an ADD COLUMN
// (as a newer Home's migration does) must not break running model queries.
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
	if err := db.Exec(`CREATE TABLE shape_t (id int primary key, name text)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO shape_t VALUES (1, 'a')`)
	read := func() error {
		var rows []shapeRecord
		if err := db.Where("id = ?", 1).Find(&rows).Error; err != nil {
			return err
		}
		var one shapeRecord
		return db.First(&one, 1).Error
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
