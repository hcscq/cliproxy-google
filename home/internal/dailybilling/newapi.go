package dailybilling

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Recommended new-api index. CPA does not create it; the database is read-only.
// The aggregate filters billed type and created_at, optionally channel, then groups model_name.
//
//	CREATE INDEX CONCURRENTLY IF NOT EXISTS logs_daily_billing_idx
//	  ON logs (type, created_at)
//	  INCLUDE (channel_id, model_name, prompt_tokens, completion_tokens, quota);

const newAPILogSQL = `
SELECT
  %s AS day,
  model_name AS model,
  COUNT(*)::bigint AS requests,
  COALESCE(SUM(prompt_tokens), 0)::bigint AS prompt_tokens,
  COALESCE(SUM(completion_tokens), 0)::bigint AS completion_tokens,
  COALESCE(SUM(quota), 0)::bigint AS quota
FROM logs
WHERE type = ?
  AND created_at >= ?
  AND created_at < ?
  %s
  AND %s
GROUP BY 1, 2`

type logScan struct {
	Day              string
	Model            string
	Requests         int64
	PromptTokens     int64 `gorm:"column:prompt_tokens"`
	CompletionTokens int64 `gorm:"column:completion_tokens"`
	Quota            int64
}

func openReadOnly(ctx context.Context, cfg Config) (*gorm.DB, error) {
	dsn, errDSN := cfg.connectionString()
	if errDSN != nil {
		return nil, errDSN
	}
	db, errOpen := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if errOpen != nil {
		return nil, errOpen
	}
	sqlDB, errSQL := db.DB()
	if errSQL != nil {
		return nil, errSQL
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if errPing := sqlDB.PingContext(pingCtx); errPing != nil {
		if errClose := sqlDB.Close(); errClose != nil {
			log.Warnf("new-api billing source close after ping failure: %s", redactSecrets(errClose.Error(), cfg.Password, dsn))
		}
		return nil, errPing
	}
	return db, nil
}

func queryNewAPILogs(ctx context.Context, db *gorm.DB, cfg Config, query Query) ([]LogRow, error) {
	if db == nil {
		return nil, ErrSourceUnavailable
	}
	cfg = cfg.normalized()
	dayExpr, fromArg, toArg := createdAtBounds(cfg, query)
	channelSQL, channelArgs := channelClause(cfg)
	predicate, predArgs := modelPredicate("model_name", cfg)
	args := []any{query.Timezone, cfg.SuccessType, fromArg, toArg}
	args = append(args, channelArgs...)
	args = append(args, predArgs...)
	var scanned []logScan
	statement := fmt.Sprintf(newAPILogSQL, dayExpr, channelSQL, predicate)
	if errScan := db.WithContext(ctx).Raw(statement, args...).Scan(&scanned).Error; errScan != nil {
		log.Warnf("new-api billing query failed: %s", redactSecrets(errScan.Error(), cfg.Password, cfg.PostgresURI))
		return nil, ErrQuery
	}
	rows := make([]LogRow, 0, len(scanned))
	for _, row := range scanned {
		rows = append(rows, LogRow{
			Day:              row.Day,
			Model:            row.Model,
			Requests:         row.Requests,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			Quota:            row.Quota,
		})
	}
	return rows, nil
}

func channelClause(cfg Config) (string, []any) {
	ids := cfg.channelIDs()
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return "AND channel_id = ?", []any{ids[0]}
	default:
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		return "AND channel_id IN (" + strings.Join(placeholders, ",") + ")", args
	}
}

func createdAtBounds(cfg Config, query Query) (dayExpr string, fromArg, toArg any) {
	switch cfg.CreatedAt {
	case "timestamptz":
		return "to_char(created_at AT TIME ZONE ?, 'YYYY-MM-DD')", query.From, query.To
	case "unix_ms":
		return "to_char(to_timestamp(created_at / 1000.0) AT TIME ZONE ?, 'YYYY-MM-DD')", query.From.UnixMilli(), query.To.UnixMilli()
	default:
		return "to_char(to_timestamp(created_at) AT TIME ZONE ?, 'YYYY-MM-DD')", query.From.Unix(), query.To.Unix()
	}
}
