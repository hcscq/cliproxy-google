package cluster

import (
	"context"
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
	for _, field := range []string{"FailBodyTruncated", "FailBodySize", "ThinkingParams", "ThinkingNote", "StreamID"} {
		if !m.HasColumn(&UsageRecord{}, field) {
			if err := m.AddColumn(&UsageRecord{}, field); err != nil {
				return err
			}
		}
	}
	return nil
}
