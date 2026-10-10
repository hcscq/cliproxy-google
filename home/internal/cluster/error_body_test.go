package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/router-for-me/CLIProxyAPIHome/internal/errorbody"
	"gorm.io/gorm"
)

func bigErrorJSON(n int) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 503, "status": "UNAVAILABLE", "message": "No capacity", "details": []any{map[string]any{"reason": "MODEL_CAPACITY_EXHAUSTED", "pad": strings.Repeat("x", n)}}}})
	return string(b)
}

func usagePayloadWithFail(body string) string {
	b, _ := json.Marshal(map[string]any{"timestamp": "2026-10-10T10:00:00Z", "provider": "antigravity", "model": "gemini-3.1-pro-low", "failed": true, "fail": map[string]any{"status_code": 503, "body": body}})
	return string(b)
}

func TestUsageRecordKeepsFullErrorBodyUnderCap(t *testing.T) {
	errorbody.SetMaxBytes(0)
	body := bigErrorJSON(2000) // well over the old 500 byte preview
	rec, err := UsageRecordFromPayload(usagePayloadWithFail(body), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.FailBody != body || rec.FailBodyTruncated || rec.FailBodySize != len(body) {
		t.Fatalf("truncated=%v size=%d len=%d", rec.FailBodyTruncated, rec.FailBodySize, len(rec.FailBody))
	}
	if !json.Valid([]byte(usageObservabilityBodyPreview(rec.FailBody))) {
		t.Fatal("preview is not valid JSON")
	}
}

func TestUsageRecordTruncatesOverCap(t *testing.T) {
	errorbody.SetMaxBytes(1024)
	t.Cleanup(func() { errorbody.SetMaxBytes(0) })
	body := bigErrorJSON(5000)
	rec, err := UsageRecordFromPayload(usagePayloadWithFail(body), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.FailBodyTruncated || rec.FailBodySize != len(body) || len(rec.FailBody) != 1024 {
		t.Fatalf("truncated=%v size=%d stored=%d", rec.FailBodyTruncated, rec.FailBodySize, len(rec.FailBody))
	}
	if len(string(rec.PayloadJSON)) > 4096 {
		t.Fatalf("raw payload copy not capped: %d", len(rec.PayloadJSON))
	}
}

func TestCapErrorBodyUTF8Boundary(t *testing.T) {
	errorbody.SetMaxBytes(4)
	t.Cleanup(func() { errorbody.SetMaxBytes(0) })
	out, trunc, size := capErrorBody("ab容量")
	if !trunc || size != 8 || out != "ab" {
		t.Fatalf("out=%q trunc=%v size=%d", out, trunc, size)
	}
}

func TestEnsureUsageFailBodyColumnsAddsMissingColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE usage (id integer primary key, fail_body text)`).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := ensureUsageFailBodyColumns(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	m := db.Migrator()
	if !m.HasColumn(&UsageRecord{}, "fail_body_truncated") || !m.HasColumn(&UsageRecord{}, "fail_body_size") {
		t.Fatal("columns not added")
	}
}
