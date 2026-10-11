package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Request log (table "usage") partitioning and retention.
//
// On Postgres the table is RANGE-partitioned by "timestamp" into daily
// partitions named usage_pYYYYMMDD plus a default partition (usage_p_default)
// that catches rows outside every daily range (e.g. imported history). Old
// days are dropped as whole partitions, which is O(1) and does not bloat the
// table. On SQLite (and on an unpartitioned table) retention falls back to
// batched DELETEs.

const (
	usagePartitionPrefix   = "usage_p"
	usageDefaultPartition  = "usage_p_default"
	usageLegacyTable       = "usage_legacy"
	usagePartitionsAhead   = 3
	defaultRetentionDays   = 30
	usageRetentionLockKey  = 7402117301
	usageRetentionDeleteN  = 5000
	usageMaintenanceTicker = time.Hour
)

// RequestLogRetentionDays normalizes the configured retention: 0 = default
// (30 days), negative = keep forever (returns 0).
func RequestLogRetentionDays(configured int) int {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return defaultRetentionDays
	default:
		return configured
	}
}

func usagePartitionName(day time.Time) string {
	return usagePartitionPrefix + day.UTC().Format("20060102")
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// usageIsPartitioned reports whether "usage" is a partitioned table.
func usageIsPartitioned(tx *gorm.DB) (bool, error) {
	if tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return false, nil
	}
	var kind string
	err := tx.Raw(`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'usage' AND n.nspname = current_schema()`).Scan(&kind).Error
	return kind == "p", err
}

// convertUsageToPartitioned turns an existing plain "usage" table into a
// partitioned one inside the caller's (migration) transaction. Existing rows
// are copied and the copy verified; the original is kept as usage_legacy so it
// can be inspected or restored, and is never touched again by Home.
func convertUsageToPartitioned(ctx context.Context, tx *gorm.DB, retentionDays int) error {
	if tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	tx = tx.WithContext(contextOrBackground(ctx))
	var kind string
	if err := tx.Raw(`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'usage' AND n.nspname = current_schema()`).Scan(&kind).Error; err != nil {
		return err
	}
	if kind == "p" {
		return nil
	}
	fresh := kind == ""
	if fresh {
		// Fresh database: create the plain table from the model and convert it
		// like an existing one (this also re-homes the id sequence); the empty
		// legacy table is dropped at the end.
		if err := tx.Migrator().CreateTable(&UsageRecord{}); err != nil {
			return err
		}
	}
	var legacyExists bool
	if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, usageLegacyTable).Scan(&legacyExists).Error; err != nil {
		return err
	}
	if legacyExists {
		return fmt.Errorf("cannot partition usage: table %s already exists", usageLegacyTable)
	}
	if err := tx.Exec(`LOCK TABLE "usage" IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
		return err
	}
	var bounds struct {
		Rows int64
		Min  *time.Time
		Max  *time.Time
	}
	if err := tx.Raw(`SELECT count(*) AS rows, min("timestamp") AS min, max("timestamp") AS max FROM "usage"`).Scan(&bounds).Error; err != nil {
		return err
	}
	var seq string
	_ = tx.Raw(`SELECT coalesce(pg_get_serial_sequence('"usage"', 'id'), '')`).Scan(&seq).Error
	if err := tx.Exec(`ALTER TABLE "usage" RENAME TO ` + usageLegacyTable).Error; err != nil {
		return err
	}
	// Free the index / constraint names for the new parent (GORM looks them up by name).
	var indexes []string
	if err := tx.Raw(`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ?`, usageLegacyTable).Scan(&indexes).Error; err != nil {
		return err
	}
	for i, name := range indexes {
		newName := fmt.Sprintf("usage_legacy_idx_%d", i)
		if name == "usage_pkey" {
			if err := tx.Exec(`ALTER TABLE ` + usageLegacyTable + ` RENAME CONSTRAINT usage_pkey TO usage_legacy_pkey`).Error; err != nil {
				return err
			}
			continue
		}
		if err := tx.Exec(`ALTER INDEX ` + quoteIdent(name) + ` RENAME TO ` + newName).Error; err != nil {
			return err
		}
	}
	if seq != "" {
		if err := tx.Exec(`ALTER SEQUENCE ` + seq + ` OWNED BY NONE`).Error; err != nil {
			return err
		}
	}
	minT, maxT := time.Now(), time.Now()
	if bounds.Min != nil {
		minT = *bounds.Min
	}
	if bounds.Max != nil {
		maxT = *bounds.Max
	}
	if err := createPartitionedUsageTable(tx, minT, maxT, retentionDays, usageLegacyTable); err != nil {
		return err
	}
	if seq != "" {
		if err := tx.Exec(`ALTER SEQUENCE ` + seq + ` OWNED BY "usage".id`).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`INSERT INTO "usage" SELECT * FROM ` + usageLegacyTable).Error; err != nil {
		return fmt.Errorf("copy usage rows into partitioned table: %w", err)
	}
	var copied int64
	if err := tx.Raw(`SELECT count(*) FROM "usage"`).Scan(&copied).Error; err != nil {
		return err
	}
	if copied != bounds.Rows {
		return fmt.Errorf("usage partition copy mismatch: %d rows copied, %d expected", copied, bounds.Rows)
	}
	if bounds.Rows == 0 {
		// Nothing to keep for inspection.
		if err := tx.Exec(`DROP TABLE ` + usageLegacyTable).Error; err != nil {
			return err
		}
		if !fresh {
			log.Info("request log (empty) converted to daily partitions")
		}
		return nil
	}
	log.WithFields(log.Fields{"rows": copied, "legacy_table": usageLegacyTable}).Info("request log converted to daily partitions")
	return nil
}

// createPartitionedUsageTable creates the partitioned parent (copying the
// column layout of likeTable)
// plus daily partitions covering [from, to + ahead] within retention.
func createPartitionedUsageTable(tx *gorm.DB, from, to time.Time, retentionDays int, likeTable string) error {
	src := likeTable
	if err := tx.Exec(`CREATE TABLE "usage" (LIKE ` + src + ` INCLUDING DEFAULTS INCLUDING GENERATED) PARTITION BY RANGE ("timestamp")`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`ALTER TABLE "usage" ADD PRIMARY KEY (id, "timestamp")`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE TABLE IF NOT EXISTS ` + usageDefaultPartition + ` PARTITION OF "usage" DEFAULT`).Error; err != nil {
		return err
	}
	start := utcDay(from)
	if retentionDays > 0 {
		if floor := utcDay(time.Now()).AddDate(0, 0, -retentionDays); start.Before(floor) {
			start = floor
		}
	}
	end := utcDay(to)
	if now := utcDay(time.Now()); now.After(end) {
		end = now
	}
	end = end.AddDate(0, 0, usagePartitionsAhead)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if err := createUsageDayPartition(tx, day); err != nil {
			return err
		}
	}
	return nil
}

func createUsageDayPartition(tx *gorm.DB, day time.Time) error {
	name := usagePartitionName(day)
	return tx.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF "usage" FOR VALUES FROM ('%s') TO ('%s')`,
		name, day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339))).Error
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// UsagePartitionInfo describes one partition for the panel.
type UsagePartitionInfo struct {
	Name      string    `json:"name"`
	From      time.Time `json:"from,omitempty"`
	To        time.Time `json:"to,omitempty"`
	IsDefault bool      `json:"is_default,omitempty"`
	Rows      int64     `json:"rows"`
	Bytes     int64     `json:"bytes"`
}

// UsageRetentionStatus is what the panel shows about the request log storage.
type UsageRetentionStatus struct {
	Backend         string               `json:"backend"`
	Partitioned     bool                 `json:"partitioned"`
	RetentionDays   int                  `json:"retention_days"`
	OldestTimestamp *time.Time           `json:"oldest_timestamp,omitempty"`
	TotalRows       int64                `json:"total_rows"`
	TotalBytes      int64                `json:"total_bytes"`
	LegacyTable     bool                 `json:"legacy_table"`
	LegacyRows      int64                `json:"legacy_rows,omitempty"`
	Partitions      []UsagePartitionInfo `json:"partitions,omitempty"`
	LastMaintenance *time.Time           `json:"last_maintenance,omitempty"`
	LastDropped     []string             `json:"last_dropped,omitempty"`
	LastDeleted     int64                `json:"last_deleted,omitempty"`
	LastError       string               `json:"last_error,omitempty"`
}

var usageMaint struct {
	at      time.Time
	dropped []string
	deleted int64
	err     string
}

// MaintainUsageRetention creates upcoming partitions, moves stray rows out of
// the default partition, and drops / deletes data older than retention.
// Safe to run concurrently from every replica (advisory lock).
func (r *Repository) MaintainUsageRetention(ctx context.Context, retentionDays int) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	ctx = contextOrBackground(ctx)
	now := time.Now()
	var dropped []string
	var deleted int64
	errRun := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		partitioned, err := usageIsPartitioned(tx)
		if err != nil {
			return err
		}
		if !partitioned {
			if retentionDays <= 0 {
				return nil
			}
			cutoff := now.AddDate(0, 0, -retentionDays)
			for {
				res := tx.Exec(`DELETE FROM "usage" WHERE id IN (SELECT id FROM "usage" WHERE "timestamp" < ? LIMIT ?)`, cutoff, usageRetentionDeleteN)
				if res.Error != nil {
					return res.Error
				}
				deleted += res.RowsAffected
				if res.RowsAffected < usageRetentionDeleteN {
					return nil
				}
			}
		}
		var locked bool
		if err := tx.Raw(`SELECT pg_try_advisory_xact_lock(?)`, usageRetentionLockKey).Scan(&locked).Error; err != nil || !locked {
			return err
		}
		today := utcDay(now)
		for i := -1; i <= usagePartitionsAhead; i++ {
			if err := ensureUsageDayPartition(tx, today.AddDate(0, 0, i)); err != nil {
				return err
			}
		}
		if retentionDays <= 0 {
			return nil
		}
		cutoff := today.AddDate(0, 0, -retentionDays)
		var parts []string
		if err := tx.Raw(`SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
			WHERE p.relname = 'usage' AND p.relnamespace = to_regnamespace(current_schema()) AND c.relname LIKE 'usage_p________' ORDER BY c.relname`).Scan(&parts).Error; err != nil {
			return err
		}
		for _, name := range parts {
			day, errDay := time.Parse("20060102", strings.TrimPrefix(name, usagePartitionPrefix))
			if errDay != nil || day.AddDate(0, 0, 1).After(cutoff) {
				continue
			}
			// Partition's upper bound (day+1) <= cutoff: everything in it is expired.
			if err := tx.Exec(`DROP TABLE ` + name).Error; err != nil {
				return err
			}
			dropped = append(dropped, name)
		}
		res := tx.Exec(`DELETE FROM `+usageDefaultPartition+` WHERE "timestamp" < ?`, cutoff)
		deleted += res.RowsAffected
		return res.Error
	})
	usageMaint.at, usageMaint.dropped, usageMaint.deleted = now, dropped, deleted
	usageMaint.err = ""
	if errRun != nil {
		usageMaint.err = errRun.Error()
	} else if len(dropped) > 0 || deleted > 0 {
		log.WithFields(log.Fields{"dropped_partitions": dropped, "deleted_rows": deleted, "retention_days": retentionDays}).Info("request log retention applied")
	}
	return errRun
}

// ensureUsageDayPartition creates a day partition; if the default partition
// already holds rows for that day they are moved into it first.
func ensureUsageDayPartition(tx *gorm.DB, day time.Time) error {
	name := usagePartitionName(day)
	var exists bool
	if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error; err != nil || exists {
		return err
	}
	from, to := day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339)
	var stray int64
	if err := tx.Raw(`SELECT count(*) FROM `+usageDefaultPartition+` WHERE "timestamp" >= ? AND "timestamp" < ?`, from, to).Scan(&stray).Error; err != nil {
		return err
	}
	if stray == 0 {
		return createUsageDayPartition(tx, day)
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE %s (LIKE "usage" INCLUDING DEFAULTS INCLUDING GENERATED)`, name),
		fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s WHERE "timestamp" >= '%s' AND "timestamp" < '%s'`, name, usageDefaultPartition, from, to),
		fmt.Sprintf(`DELETE FROM %s WHERE "timestamp" >= '%s' AND "timestamp" < '%s'`, usageDefaultPartition, from, to),
		fmt.Sprintf(`ALTER TABLE "usage" ATTACH PARTITION %s FOR VALUES FROM ('%s') TO ('%s')`, name, from, to),
	}
	for _, s := range stmts {
		if err := tx.Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}

// UsageRetentionStatus reports partitions and retention for the panel.
func (r *Repository) UsageRetentionStatus(ctx context.Context, retentionDays int) (UsageRetentionStatus, error) {
	out := UsageRetentionStatus{RetentionDays: retentionDays}
	db, err := r.database()
	if err != nil {
		return out, err
	}
	db = db.WithContext(contextOrBackground(ctx))
	if db.Dialector != nil {
		out.Backend = db.Dialector.Name()
	}
	if !usageMaint.at.IsZero() {
		at := usageMaint.at
		out.LastMaintenance, out.LastDropped, out.LastDeleted, out.LastError = &at, usageMaint.dropped, usageMaint.deleted, usageMaint.err
	}
	var oldest struct{ T *time.Time }
	_ = db.Raw(`SELECT min("timestamp") AS t FROM "usage"`).Scan(&oldest).Error
	out.OldestTimestamp = oldest.T
	partitioned, err := usageIsPartitioned(db)
	if err != nil {
		return out, err
	}
	out.Partitioned = partitioned
	if !partitioned {
		_ = db.Raw(`SELECT count(*) FROM "usage"`).Scan(&out.TotalRows).Error
		return out, nil
	}
	type row struct {
		Name  string
		Rows  int64
		Bytes int64
	}
	var rows []row
	if err := db.Raw(`SELECT c.relname AS name, greatest(c.reltuples, 0)::bigint AS rows, pg_total_relation_size(c.oid) AS bytes
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'usage' AND p.relnamespace = to_regnamespace(current_schema()) ORDER BY c.relname DESC`).Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, x := range rows {
		info := UsagePartitionInfo{Name: x.Name, Rows: x.Rows, Bytes: x.Bytes}
		if x.Name == usageDefaultPartition {
			info.IsDefault = true
		} else if day, errDay := time.Parse("20060102", strings.TrimPrefix(x.Name, usagePartitionPrefix)); errDay == nil {
			info.From, info.To = day, day.AddDate(0, 0, 1)
		}
		// reltuples is -1/0 until the first ANALYZE; count small partitions exactly.
		if x.Rows <= 0 && x.Bytes < 64<<20 {
			_ = db.Raw(`SELECT count(*) FROM ` + quoteIdent(x.Name)).Scan(&info.Rows).Error
		}
		out.TotalRows += info.Rows
		out.TotalBytes += info.Bytes
		out.Partitions = append(out.Partitions, info)
	}
	var legacy bool
	_ = db.Raw(`SELECT to_regclass(?) IS NOT NULL`, usageLegacyTable).Scan(&legacy).Error
	out.LegacyTable = legacy
	if legacy {
		_ = db.Raw(`SELECT count(*) FROM ` + usageLegacyTable).Scan(&out.LegacyRows).Error
	}
	return out, nil
}

// StartUsageRetention runs maintenance at start-up and hourly.
func (r *Repository) StartUsageRetention(ctx context.Context, retentionDays func() int) {
	go func() {
		run := func() {
			mctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := r.MaintainUsageRetention(mctx, retentionDays()); err != nil && ctx.Err() == nil {
				log.WithError(err).Warn("request log retention maintenance failed")
			}
		}
		run()
		t := time.NewTicker(usageMaintenanceTicker)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
