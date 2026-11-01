package dailybilling

import (
	"context"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Recommended CPA index, not created automatically. usage is range-partitioned
// on Postgres. CREATE INDEX CONCURRENTLY cannot run on the parent, so build each
// partition concurrently, create the parent with ON ONLY, then ATTACH:
//
//	CREATE INDEX CONCURRENTLY IF NOT EXISTS usage_pYYYYMMDD_daily_billing_idx
//	  ON usage_pYYYYMMDD (timestamp)
//	  INCLUDE (auth_index, total_tokens)
//	  WHERE failed = false;
//	CREATE INDEX IF NOT EXISTS usage_daily_billing_idx
//	  ON ONLY usage (timestamp)
//	  INCLUDE (auth_index, total_tokens)
//	  WHERE failed = false;
//	ALTER INDEX usage_daily_billing_idx ATTACH PARTITION usage_pYYYYMMDD_daily_billing_idx;
//
// The INCLUDE columns cover the daily-billing CTE. auth.uuid is already the PK.

// cpaPostgresSQL aggregates per credential+day first, then joins auth once per
// credential instead of per request. At ~1M successful rows/day that keeps the
// HashAggregate under a few seconds instead of sorting every request for
// COUNT(DISTINCT) against the 30s request deadline.
const cpaPostgresSQL = `
WITH per_cred AS (
  SELECT
    (u.timestamp AT TIME ZONE ?)::date AS day,
    NULLIF(u.auth_index, '') AS auth_index,
    COUNT(*)::bigint AS requests,
    COALESCE(SUM(u.total_tokens), 0)::bigint AS total_tokens
  FROM usage AS u
  WHERE u.failed = FALSE
    AND u.timestamp >= ?
    AND u.timestamp < ?
    AND %s
  GROUP BY 1, 2
)
SELECT
  to_char(p.day, 'YYYY-MM-DD') AS day,
  CASE lower(COALESCE(a.auth_json->'metadata'->>'subscription_tier', ''))
    WHEN ? THEN 'pro'
    WHEN ? THEN 'ultra'
    ELSE 'other'
  END AS tier,
  COALESCE(SUM(p.requests), 0)::bigint AS requests,
  COALESCE(SUM(p.total_tokens), 0)::bigint AS total_tokens,
  COUNT(*) FILTER (WHERE p.auth_index IS NOT NULL)::bigint AS active_credentials
FROM per_cred AS p
LEFT JOIN auth AS a ON a.deleted_at IS NULL AND a.uuid = p.auth_index
GROUP BY 1, 2`

const cpaSQLiteSQL = `
WITH per_cred AS (
  SELECT
    substr(u.timestamp, 1, 10) AS day,
    NULLIF(u.auth_index, '') AS auth_index,
    COUNT(*) AS requests,
    COALESCE(SUM(u.total_tokens), 0) AS total_tokens
  FROM usage AS u
  WHERE u.failed = 0
    AND u.timestamp >= ?
    AND u.timestamp < ?
    AND %s
  GROUP BY 1, 2
)
SELECT
  p.day AS day,
  CASE lower(COALESCE(json_extract(a.auth_json, '$.metadata.subscription_tier'), ''))
    WHEN ? THEN 'pro'
    WHEN ? THEN 'ultra'
    ELSE 'other'
  END AS tier,
  COALESCE(SUM(p.requests), 0) AS requests,
  COALESCE(SUM(p.total_tokens), 0) AS total_tokens,
  SUM(CASE WHEN p.auth_index IS NOT NULL THEN 1 ELSE 0 END) AS active_credentials
FROM per_cred AS p
LEFT JOIN auth AS a ON a.deleted_at IS NULL AND a.uuid = p.auth_index
GROUP BY 1, 2`

type tierScan struct {
	Day               string
	Tier              string
	Requests          int64
	TotalTokens       int64 `gorm:"column:total_tokens"`
	ActiveCredentials int64 `gorm:"column:active_credentials"`
}

func queryCPATiers(ctx context.Context, db *gorm.DB, cfg Config, query Query) ([]TierRow, error) {
	if db == nil || db.Dialector == nil {
		return nil, fmt.Errorf("%w: home database is not open", ErrQuery)
	}
	cfg = cfg.normalized()
	predicate, predArgs := modelPredicate("u.model", cfg)
	sqlText := cpaSQLiteSQL
	timeFrom := any(query.From.UTC().Format("2006-01-02 15:04:05"))
	timeTo := any(query.To.UTC().Format("2006-01-02 15:04:05"))
	postgres := db.Dialector.Name() == "postgres"
	if postgres {
		sqlText = cpaPostgresSQL
		timeFrom = query.From
		timeTo = query.To
	}
	args := make([]any, 0, 7+len(predArgs))
	if postgres {
		args = append(args, query.Timezone)
	}
	args = append(args, timeFrom, timeTo)
	args = append(args, predArgs...)
	args = append(args, strings.ToLower(cfg.ProTier), strings.ToLower(cfg.UltraTier))
	var scanned []tierScan
	if errScan := db.WithContext(ctx).Raw(fmt.Sprintf(sqlText, predicate), args...).Scan(&scanned).Error; errScan != nil {
		log.Warnf("cpa billing tier query failed: %s", errScan.Error())
		return nil, ErrQuery
	}
	rows := make([]TierRow, 0, len(scanned))
	for _, row := range scanned {
		rows = append(rows, TierRow{
			Day:               row.Day,
			Tier:              row.Tier,
			Requests:          row.Requests,
			TotalTokens:       row.TotalTokens,
			ActiveCredentials: row.ActiveCredentials,
		})
	}
	return rows, nil
}

func modelPredicate(column string, cfg Config) (string, []any) {
	var parts []string
	var args []any
	models := cleanModels(cfg.Models)
	switch {
	case len(models) > 0:
		placeholders := make([]string, len(models))
		for i, model := range models {
			placeholders[i] = "?"
			args = append(args, model)
		}
		parts = append(parts, fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ",")))
	case strings.TrimSpace(cfg.ModelPrefix) != "":
		parts = append(parts, fmt.Sprintf("%s LIKE ? ESCAPE '\\'", column))
		args = append(args, likePrefix(cfg.ModelPrefix))
	}
	if exclude := strings.TrimSpace(cfg.ModelExclude); exclude != "" {
		parts = append(parts, fmt.Sprintf("%s NOT LIKE ? ESCAPE '\\'", column))
		args = append(args, likeContains(exclude))
	}
	if len(parts) == 0 {
		return "TRUE", nil
	}
	return strings.Join(parts, " AND "), args
}

func likePrefix(value string) string {
	return escapeLike(value) + "%"
}

func likeContains(value string) string {
	return "%" + escapeLike(value)
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
