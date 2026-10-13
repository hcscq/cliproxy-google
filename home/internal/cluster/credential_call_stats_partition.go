package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Per-credential stat tables are RANGE-partitioned by bucket into daily
// partitions on Postgres (<table>_pYYYYMMDD + <table>_p_default) so retention
// drops whole days instead of DELETE-ing millions of rows at 10k credentials.
// Stats are derived from the request log, so conversion copies rows in one
// transaction and drops the old table only after the copy count matches.

var callStatPartitionedTables = []struct{ name, pk string }{
	{"credential_call_stat", `"auth_index","bucket","class"`},
	{"credential_model_stat", `"auth_index","bucket","model","class"`},
}

const callStatPartitionsAhead = 2

func callStatPartitionName(table string, day time.Time) string {
	return table + "_p" + day.UTC().Format("20060102")
}

func tableRelkind(tx *gorm.DB, table string) (string, error) {
	var kind string
	err := tx.Raw(`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`, table).Scan(&kind).Error
	return kind, err
}

// partitionCallStatTables converts the plain stat tables (created by
// ensureCredentialCallStatTables) into partitioned ones. Idempotent.
func partitionCallStatTables(ctx context.Context, db *gorm.DB) error {
	if db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	for _, t := range callStatPartitionedTables {
		t := t
		err := db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, callStatAdvisoryLock).Error; err != nil {
				return err
			}
			kind, err := tableRelkind(tx, t.name)
			if err != nil || kind == "p" {
				return err
			}
			old := t.name + "_unpartitioned"
			var rows int64
			if kind != "" {
				if err := tx.Exec(`LOCK TABLE ` + quoteIdent(t.name) + ` IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
					return err
				}
				if err := tx.Raw(`SELECT count(*) FROM ` + quoteIdent(t.name)).Scan(&rows).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE ` + quoteIdent(t.name) + ` RENAME TO ` + quoteIdent(old)).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE ` + quoteIdent(old) + ` RENAME CONSTRAINT ` + quoteIdent(t.name+"_pkey") + ` TO ` + quoteIdent(old+"_pkey")).Error; err != nil {
					return err
				}
				if err := tx.Exec(`DROP INDEX IF EXISTS ` + quoteIdent("idx_"+t.name+"_bucket")).Error; err != nil {
					return err
				}
			}
			cols := `"auth_index" text NOT NULL, "bucket" timestamptz NOT NULL, "class" text NOT NULL, "calls" bigint NOT NULL`
			if strings.Contains(t.pk, "model") {
				cols = `"auth_index" text NOT NULL, "bucket" timestamptz NOT NULL, "model" text NOT NULL, "class" text NOT NULL, "calls" bigint NOT NULL`
			}
			if err := tx.Exec(fmt.Sprintf(`CREATE TABLE %s (%s, PRIMARY KEY (%s)) PARTITION BY RANGE ("bucket")`, quoteIdent(t.name), cols, t.pk)).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS ` + quoteIdent("idx_"+t.name+"_bucket") + ` ON ` + quoteIdent(t.name) + ` ("bucket")`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE TABLE IF NOT EXISTS ` + quoteIdent(t.name+"_p_default") + ` PARTITION OF ` + quoteIdent(t.name) + ` DEFAULT`).Error; err != nil {
				return err
			}
			if err := ensureCallStatDayPartitions(tx, t.name); err != nil {
				return err
			}
			if kind == "" {
				return nil
			}
			cut := utcDay(time.Now()).Add(-callStatRetention - 24*time.Hour)
			if err := tx.Exec(`INSERT INTO `+quoteIdent(t.name)+` SELECT * FROM `+quoteIdent(old)+` WHERE bucket >= ?`, cut).Error; err != nil {
				return fmt.Errorf("copy %s: %w", t.name, err)
			}
			var copied, expected int64
			if err := tx.Raw(`SELECT count(*) FROM ` + quoteIdent(t.name)).Scan(&copied).Error; err != nil {
				return err
			}
			if err := tx.Raw(`SELECT count(*) FROM `+quoteIdent(old)+` WHERE bucket >= ?`, cut).Scan(&expected).Error; err != nil {
				return err
			}
			if copied != expected {
				return fmt.Errorf("%s partition copy mismatch: %d copied, %d expected", t.name, copied, expected)
			}
			log.WithFields(log.Fields{"table": t.name, "rows": copied, "total": rows}).Info("credential stats converted to daily partitions")
			return tx.Exec(`DROP TABLE ` + quoteIdent(old)).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ensureCallStatDayPartitions creates partitions from (retention ago) to (today + ahead).
func ensureCallStatDayPartitions(tx *gorm.DB, table string) error {
	start := utcDay(time.Now().Add(-callStatRetention))
	end := utcDay(time.Now()).AddDate(0, 0, callStatPartitionsAhead)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		name := callStatPartitionName(table, day)
		var exists bool
		if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error; err != nil {
			return err
		}
		if exists {
			continue
		}
		// Move matching rows out of the default partition first (if any), else CREATE fails.
		from, to := day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339)
		var stray int64
		_ = tx.Raw(`SELECT count(*) FROM `+quoteIdent(table+"_p_default")+` WHERE bucket >= ? AND bucket < ?`, day, day.AddDate(0, 0, 1)).Scan(&stray).Error
		if stray > 0 {
			tmp := name + "_stray"
			if err := tx.Exec(fmt.Sprintf(`CREATE TEMP TABLE %s ON COMMIT DROP AS SELECT * FROM %s WHERE bucket >= '%s' AND bucket < '%s'`, quoteIdent(tmp), quoteIdent(table+"_p_default"), from, to)).Error; err != nil {
				return err
			}
			if err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE bucket >= '%s' AND bucket < '%s'`, quoteIdent(table+"_p_default"), from, to)).Error; err != nil {
				return err
			}
			if err := tx.Exec(fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`, quoteIdent(name), quoteIdent(table), from, to)).Error; err != nil {
				return err
			}
			if err := tx.Exec(fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s`, quoteIdent(table), quoteIdent(tmp))).Error; err != nil {
				return err
			}
			continue
		}
		if err := tx.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`, quoteIdent(name), quoteIdent(table), from, to)).Error; err != nil {
			return err
		}
	}
	return nil
}

// maintainCallStatPartitions creates upcoming days and drops expired ones.
// Returns handled=false when the tables are not partitioned (caller DELETEs).
func maintainCallStatPartitions(ctx context.Context, db *gorm.DB) (bool, error) {
	if db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return false, nil
	}
	handled := true
	err := db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, callStatAdvisoryLock).Error; err != nil {
			return err
		}
		cutDay := utcDay(time.Now().Add(-callStatRetention))
		for _, t := range callStatPartitionedTables {
			kind, err := tableRelkind(tx, t.name)
			if err != nil {
				return err
			}
			if kind != "p" {
				handled = false
				return nil
			}
			if err := ensureCallStatDayPartitions(tx, t.name); err != nil {
				return err
			}
			var parts []string
			if err := tx.Raw(`SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
				WHERE p.relname = ?`, t.name).Scan(&parts).Error; err != nil {
				return err
			}
			prefix := t.name + "_p"
			for _, part := range parts {
				suffix := strings.TrimPrefix(part, prefix)
				day, err := time.Parse("20060102", suffix)
				if err != nil || !day.Before(cutDay) {
					continue
				}
				if err := tx.Exec(`DROP TABLE IF EXISTS ` + quoteIdent(part)).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec(`DELETE FROM `+quoteIdent(t.name+"_p_default")+` WHERE bucket < ?`, cutDay).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return handled, err
}
