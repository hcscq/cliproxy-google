package dailybilling

import (
	"context"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Recommended CPA index, not created automatically. usage is range-partitioned
// on Postgres; create it on the parent table so each partition inherits it.
// auth.uuid is already the primary key, which is the join used here.
//
//	CREATE INDEX CONCURRENTLY IF NOT EXISTS usage_gemini4_billing_idx
//	  ON usage (timestamp, model)
//	  WHERE failed = false;

const cpaPostgresSQL = `
SELECT
  to_char(u.timestamp AT TIME ZONE ?, 'YYYY-MM-DD') AS day,
  CASE lower(COALESCE(a.auth_json->'metadata'->>'subscription_tier', ''))
    WHEN ? THEN 'pro'
    WHEN ? THEN 'ultra'
    ELSE 'other'
  END AS tier,
  COUNT(*)::bigint AS requests,
  COALESCE(SUM(u.total_tokens), 0)::bigint AS total_tokens
FROM usage AS u
LEFT JOIN auth AS a ON a.deleted_at IS NULL AND a.uuid = u.auth_index
WHERE u.failed = FALSE
  AND u.timestamp >= ?
  AND u.timestamp < ?
  AND %s
GROUP BY 1, 2`

const cpaSQLiteSQL = `
SELECT
  substr(u.timestamp, 1, 10) AS day,
  CASE lower(COALESCE(json_extract(a.auth_json, '$.metadata.subscription_tier'), ''))
    WHEN ? THEN 'pro'
    WHEN ? THEN 'ultra'
    ELSE 'other'
  END AS tier,
  COUNT(*) AS requests,
  COALESCE(SUM(u.total_tokens), 0) AS total_tokens
FROM usage AS u
LEFT JOIN auth AS a ON a.deleted_at IS NULL AND a.uuid = u.auth_index
WHERE u.failed = 0
  AND u.timestamp >= ?
  AND u.timestamp < ?
  AND %s
GROUP BY 1, 2`

type tierScan struct {
	Day         string
	Tier        string
	Requests    int64
	TotalTokens int64 `gorm:"column:total_tokens"`
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
	if db.Dialector.Name() == "postgres" {
		sqlText = cpaPostgresSQL
		timeFrom = query.From
		timeTo = query.To
	}
	args := make([]any, 0, 6+len(predArgs))
	if db.Dialector.Name() == "postgres" {
		args = append(args, query.Timezone)
	}
	args = append(args, strings.ToLower(cfg.ProTier), strings.ToLower(cfg.UltraTier), timeFrom, timeTo)
	args = append(args, predArgs...)
	var scanned []tierScan
	if errScan := db.WithContext(ctx).Raw(fmt.Sprintf(sqlText, predicate), args...).Scan(&scanned).Error; errScan != nil {
		log.Warnf("cpa billing tier query failed: %s", errScan.Error())
		return nil, ErrQuery
	}
	rows := make([]TierRow, 0, len(scanned))
	for _, row := range scanned {
		rows = append(rows, TierRow{
			Day:         row.Day,
			Tier:        row.Tier,
			Requests:    row.Requests,
			TotalTokens: row.TotalTokens,
		})
	}
	return rows, nil
}

func modelPredicate(column string, cfg Config) (string, []any) {
	exclude := likeContains(cfg.ModelExclude)
	models := cleanModels(cfg.Models)
	if len(models) > 0 {
		placeholders := make([]string, len(models))
		args := make([]any, 0, len(models)+1)
		for i, model := range models {
			placeholders[i] = "?"
			args = append(args, model)
		}
		args = append(args, exclude)
		return fmt.Sprintf("%s IN (%s) AND %s NOT LIKE ? ESCAPE '\\'", column, strings.Join(placeholders, ","), column), args
	}
	return fmt.Sprintf("%s LIKE ? ESCAPE '\\' AND %s NOT LIKE ? ESCAPE '\\'", column, column), []any{likePrefix(cfg.ModelPrefix), exclude}
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
