package cluster

import (
	"context"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Per-credential call stats.
//
// The usage table is far too large to scan per panel request at ~100k RPM, so a
// rollup keeps counts per credential and error class: 10-minute buckets (windows)
// plus hourly per-model buckets (drawer split). Every Home runs the rollup; on
// Postgres an advisory lock + watermark row make it exactly-once.

const (
	CallStatBucket       = 10 * time.Minute
	callStatRetention    = 8 * 24 * time.Hour
	callStatBatch        = 50000
	callStatLookback     = 3 * time.Hour // partition pruning for incremental passes
	callStatAdvisoryLock = 724311001
)

// Error classes. "ok" is a success.
const (
	CallClassOK        = "ok"
	CallClass429       = "429"
	CallClass503       = "503"
	CallClass403       = "403"
	CallClass404       = "404"
	CallClass400       = "400"
	CallClass5xx       = "5xx"
	CallClassNetwork   = "net"
	CallClassCancelled = "cancel"
	CallClassOther     = "other"
)

var netErrorRe = regexp.MustCompile(`(?i)proxy|egress|dial|connection (refused|reset)|i/o timeout|no such host|tls handshake|eof`)

// ClassifyCall maps a usage row to an error class.
func ClassifyCall(failed bool, code int, body string) string {
	if !failed {
		return CallClassOK
	}
	switch {
	case code == 429:
		return CallClass429
	case code == 503:
		return CallClass503
	case code == 403:
		return CallClass403
	case code == 404:
		return CallClass404
	case code == 400 || code == 413:
		return CallClass400
	case code == 499:
		return CallClassCancelled
	case code == 0 || ((code == 502 || code == 504) && netErrorRe.MatchString(body)):
		return CallClassNetwork
	case code >= 500:
		return CallClass5xx
	}
	return CallClassOther
}

// CredentialCallStatRecord: 10-minute buckets per credential and class.
type CredentialCallStatRecord struct {
	AuthIndex string    `gorm:"column:auth_index;primaryKey"`
	Bucket    time.Time `gorm:"column:bucket;primaryKey;index"`
	Class     string    `gorm:"column:class;primaryKey"`
	Calls     int64     `gorm:"column:calls;not null"`
}

func (CredentialCallStatRecord) TableName() string { return "credential_call_stat" }

// CredentialModelStatRecord: hourly buckets per credential, model and class.
type CredentialModelStatRecord struct {
	AuthIndex string    `gorm:"column:auth_index;primaryKey"`
	Bucket    time.Time `gorm:"column:bucket;primaryKey;index"`
	Model     string    `gorm:"column:model;primaryKey"`
	Class     string    `gorm:"column:class;primaryKey"`
	Calls     int64     `gorm:"column:calls;not null"`
}

func (CredentialModelStatRecord) TableName() string { return "credential_model_stat" }

// CredentialCallStatState holds the rollup watermark (single row id=1).
type CredentialCallStatState struct {
	ID     int   `gorm:"column:id;primaryKey"`
	LastID int64 `gorm:"column:last_id;not null"`
}

func (CredentialCallStatState) TableName() string { return "credential_call_stat_state" }

func ensureCredentialCallStatTables(ctx context.Context, db *gorm.DB) error {
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS "credential_call_stat" ("auth_index" text NOT NULL, "bucket" timestamptz NOT NULL, "class" text NOT NULL, "calls" bigint NOT NULL, PRIMARY KEY ("auth_index","bucket","class"))`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_call_stat_bucket" ON "credential_call_stat" ("bucket")`,
			`CREATE TABLE IF NOT EXISTS "credential_model_stat" ("auth_index" text NOT NULL, "bucket" timestamptz NOT NULL, "model" text NOT NULL, "class" text NOT NULL, "calls" bigint NOT NULL, PRIMARY KEY ("auth_index","bucket","model","class"))`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_model_stat_bucket" ON "credential_model_stat" ("bucket")`,
			`CREATE TABLE IF NOT EXISTS "credential_call_stat_state" ("id" bigint PRIMARY KEY, "last_id" bigint NOT NULL)`,
		} {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return tx.AutoMigrate(&CredentialCallStatRecord{}, &CredentialModelStatRecord{}, &CredentialCallStatState{})
}

type callStatRow struct {
	ID             int64
	Timestamp      time.Time
	AuthIndex      string
	Model          string
	Failed         bool
	FailStatusCode int
	Body           string
}

// RollupCredentialCallStats folds new usage rows into the stat tables. It returns
// the number of usage rows consumed.
func (r *Repository) RollupCredentialCallStats(ctx context.Context) (int, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	total := 0
	for i := 0; i < 20; i++ { // bounded catch-up per call
		n, err := r.rollupCallStatsBatch(ctx, db)
		total += n
		if err != nil || n < callStatBatch {
			return total, err
		}
	}
	return total, nil
}

func (r *Repository) rollupCallStatsBatch(ctx context.Context, db *gorm.DB) (int, error) {
	n := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pg := tx.Dialector.Name() == "postgres"
		if pg {
			var got bool
			if err := tx.Raw(`SELECT pg_try_advisory_xact_lock(?)`, callStatAdvisoryLock).Scan(&got).Error; err != nil {
				return err
			}
			if !got {
				return nil // another Home is rolling up
			}
		}
		var st CredentialCallStatState
		if err := tx.Where("id = 1").Limit(1).Find(&st).Error; err != nil {
			return err
		}
		lookback := callStatLookback
		if st.LastID == 0 {
			lookback = callStatRetention // first run: backfill
		}
		since := time.Now().UTC().Add(-lookback)
		var rows []callStatRow
		if err := tx.Raw(`SELECT id, timestamp, COALESCE(auth_index, '') AS auth_index, COALESCE(model, '') AS model, failed, fail_status_code,
			CASE WHEN failed AND fail_status_code IN (0, 502, 504) THEN substr(COALESCE(fail_body, ''), 1, 300) ELSE '' END AS body
			FROM usage WHERE id > ? AND timestamp > ? ORDER BY id LIMIT ?`, st.LastID, since, callStatBatch).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		type ck struct {
			a, c string
			b    time.Time
		}
		type mk struct {
			a, m, c string
			b       time.Time
		}
		calls := map[ck]int64{}
		models := map[mk]int64{}
		maxID := st.LastID
		for _, row := range rows {
			if row.ID > maxID {
				maxID = row.ID
			}
			if row.AuthIndex == "" {
				continue
			}
			class := ClassifyCall(row.Failed, row.FailStatusCode, row.Body)
			ts := row.Timestamp.UTC()
			calls[ck{row.AuthIndex, class, ts.Truncate(CallStatBucket)}]++
			models[mk{row.AuthIndex, strings.TrimSpace(row.Model), class, ts.Truncate(time.Hour)}]++
		}
		recs := make([]CredentialCallStatRecord, 0, len(calls))
		for k, v := range calls {
			recs = append(recs, CredentialCallStatRecord{AuthIndex: k.a, Bucket: k.b, Class: k.c, Calls: v})
		}
		mrecs := make([]CredentialModelStatRecord, 0, len(models))
		for k, v := range models {
			mrecs = append(mrecs, CredentialModelStatRecord{AuthIndex: k.a, Bucket: k.b, Model: k.m, Class: k.c, Calls: v})
		}
		if len(recs) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "auth_index"}, {Name: "bucket"}, {Name: "class"}},
				DoUpdates: clause.Assignments(map[string]any{"calls": gorm.Expr("credential_call_stat.calls + excluded.calls")}),
			}).CreateInBatches(recs, 500).Error; err != nil {
				return err
			}
		}
		if len(mrecs) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "auth_index"}, {Name: "bucket"}, {Name: "model"}, {Name: "class"}},
				DoUpdates: clause.Assignments(map[string]any{"calls": gorm.Expr("credential_model_stat.calls + excluded.calls")}),
			}).CreateInBatches(mrecs, 500).Error; err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_id"}),
		}).Create(&CredentialCallStatState{ID: 1, LastID: maxID}).Error; err != nil {
			return err
		}
		n = len(rows)
		return nil
	})
	return n, err
}

// PruneCredentialCallStats drops buckets past retention.
func (r *Repository) PruneCredentialCallStats(ctx context.Context) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	cut := time.Now().UTC().Add(-callStatRetention)
	if err := db.WithContext(ctx).Where("bucket < ?", cut).Delete(&CredentialCallStatRecord{}).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Where("bucket < ?", cut).Delete(&CredentialModelStatRecord{}).Error
}

// CredentialCallStats are counts for one credential over a window.
type CredentialCallStats struct {
	Calls   int64            `json:"calls"`
	OK      int64            `json:"ok"`
	ByClass map[string]int64 `json:"by_class"`
}

func (s *CredentialCallStats) add(class string, n int64) {
	s.Calls += n
	if class == CallClassOK {
		s.OK += n
		return
	}
	if s.ByClass == nil {
		s.ByClass = map[string]int64{}
	}
	s.ByClass[class] += n
}

// LoadCredentialCallStats sums buckets since the given time for all credentials.
func (r *Repository) LoadCredentialCallStats(ctx context.Context, since time.Time) (map[string]*CredentialCallStats, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		AuthIndex string
		Class     string
		Calls     int64
	}
	if err := db.WithContext(ctx).Raw(`SELECT auth_index, class, SUM(calls) AS calls FROM credential_call_stat WHERE bucket >= ? GROUP BY auth_index, class`, since.UTC().Truncate(CallStatBucket)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]*CredentialCallStats{}
	for _, row := range rows {
		s := out[row.AuthIndex]
		if s == nil {
			s = &CredentialCallStats{}
			out[row.AuthIndex] = s
		}
		s.add(row.Class, row.Calls)
	}
	return out, nil
}

// CredentialModelCallStats is one model's counts for a credential.
type CredentialModelCallStats struct {
	Model string `json:"model"`
	CredentialCallStats
}

// LoadCredentialModelCallStats returns per-model counts for one credential (hourly granularity).
func (r *Repository) LoadCredentialModelCallStats(ctx context.Context, authIndex string, since time.Time) ([]CredentialModelCallStats, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Model string
		Class string
		Calls int64
	}
	if err := db.WithContext(ctx).Raw(`SELECT model, class, SUM(calls) AS calls FROM credential_model_stat WHERE auth_index = ? AND bucket >= ? GROUP BY model, class`, authIndex, since.UTC().Truncate(time.Hour)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	idx := map[string]int{}
	var out []CredentialModelCallStats
	for _, row := range rows {
		i, ok := idx[row.Model]
		if !ok {
			i = len(out)
			idx[row.Model] = i
			out = append(out, CredentialModelCallStats{Model: row.Model})
		}
		out[i].add(row.Class, row.Calls)
	}
	return out, nil
}
