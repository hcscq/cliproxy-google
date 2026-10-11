package cluster

import (
	"context"
	"fmt"
	"github.com/router-for-me/CLIProxyAPIHome/internal/errorbody"
	"unicode/utf8"

	"gorm.io/gorm"
)

// capErrorBody stores the body whole when it fits; otherwise it is cut on a
// UTF-8 boundary and reported as truncated together with its original size.
func capErrorBody(body string) (string, bool, int) {
	size := len(body)
	limit := errorbody.MaxBytes()
	if size <= limit {
		return body, false, size
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut], true, size
}

// ensureUsageFailBodyColumns adds usage.fail_body_truncated/fail_body_size on
// databases whose schema version was already current before the columns existed
// (the PostgreSQL migration gate skips AutoMigrate then). Idempotent; works on
// PostgreSQL and SQLite.
func ensureUsageFailBodyColumns(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	m := db.WithContext(contextOrBackground(ctx)).Migrator()
	if !m.HasTable(&UsageRecord{}) {
		return nil
	}
	fields := []string{"FailBodyTruncated", "FailBodySize", "ThinkingParams", "ThinkingNote", "StreamID"}
	if db.Dialector.Name() == "postgres" {
		// IF NOT EXISTS: Homes starting together race on these columns; the
		// loser of a plain ADD would fail its migration and exit.
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(&UsageRecord{}); err != nil {
			return err
		}
		for _, field := range fields {
			f := stmt.Schema.LookUpField(field)
			if f == nil {
				return fmt.Errorf("usage field %s not found", field)
			}
			sql := fmt.Sprintf(`ALTER TABLE "usage" ADD COLUMN IF NOT EXISTS %q %s`, f.DBName, db.Migrator().FullDataTypeOf(f).SQL)
			if err := db.WithContext(contextOrBackground(ctx)).Exec(sql).Error; err != nil {
				return err
			}
		}
		return nil
	}
	for _, field := range fields {
		if !m.HasColumn(&UsageRecord{}, field) {
			if err := m.AddColumn(&UsageRecord{}, field); err != nil {
				return err
			}
		}
	}
	return nil
}
