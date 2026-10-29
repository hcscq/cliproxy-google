package dailybilling

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCPAQueryAggregatesByDayAndTier(t *testing.T) {
	t.Parallel()

	db, errOpen := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	sqlDB, errSQL := db.DB()
	if errSQL != nil {
		t.Fatal(errSQL)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE auth (uuid text primary key, auth_json text, deleted_at text)`,
		`CREATE TABLE usage (id integer primary key, timestamp text, failed integer, model text, total_tokens integer, auth_index text)`,
		`INSERT INTO auth (uuid, auth_json, deleted_at) VALUES
		  ('pro-1', '{"metadata":{"subscription_tier":"g1-pro-tier"}}', NULL),
		  ('ultra-1', '{"metadata":{"subscription_tier":"g1-ultra-tier"}}', NULL),
		  ('free-1', '{"metadata":{"subscription_tier":"free-tier"}}', NULL),
		  ('gone-1', '{"metadata":{"subscription_tier":"g1-pro-tier"}}', '2026-10-01')`,
		`INSERT INTO usage (id, timestamp, failed, model, total_tokens, auth_index) VALUES
		  (1, '2026-10-27 01:00:00', 0, 'gemini-4-argon-high', 700, 'pro-1'),
		  (2, '2026-10-27 02:00:00', 0, 'gemini-4-argon', 300, 'ultra-1'),
		  (3, '2026-10-27 03:00:00', 1, 'gemini-4-argon', 9999, 'pro-1'),
		  (4, '2026-10-27 04:00:00', 0, 'gemini-3-flash', 5000, 'pro-1'),
		  (5, '2026-10-28 01:00:00', 0, 'gemini-4-argon-low', 100, 'pro-1'),
		  (6, '2026-10-28 02:00:00', 0, 'gemini-4-argon-medium', 1, 'ultra-1'),
		  (7, '2026-10-28 03:00:00', 0, 'gemini-4-argon', 9, 'free-1'),
		  (8, '2026-10-28 04:00:00', 0, 'gemini-4-argon-disabled', 50, 'pro-1'),
		  (9, '2026-10-28 05:00:00', 0, 'gemini-4-argon', 40, 'gone-1'),
		  (10, '2026-10-30 00:00:00', 0, 'gemini-4-argon', 80, 'pro-1')`,
	} {
		if errExec := db.Exec(statement).Error; errExec != nil {
			t.Fatal(errExec)
		}
	}

	query, errRange := ResolveRange(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), "2026-10-27", "2026-10-28", "UTC")
	if errRange != nil {
		t.Fatal(errRange)
	}
	rows, errQuery := queryCPATiers(context.Background(), db, Config{}.normalized(), query)
	if errQuery != nil {
		t.Fatal(errQuery)
	}
	got := map[string]TierRow{}
	for _, row := range rows {
		got[row.Day+"/"+row.Tier] = row
	}
	assertTier := func(day, tier string, requests, tokens int64) {
		t.Helper()
		row, ok := got[day+"/"+tier]
		if !ok {
			t.Fatalf("missing %s/%s in %+v", day, tier, got)
		}
		if row.Requests != requests || row.TotalTokens != tokens {
			t.Fatalf("%s/%s = %+v, want requests %d tokens %d", day, tier, row, requests, tokens)
		}
	}
	assertTier("2026-10-27", TierPro, 1, 700)
	assertTier("2026-10-27", TierUltra, 1, 300)
	assertTier("2026-10-28", TierPro, 1, 100)
	assertTier("2026-10-28", TierUltra, 1, 1)
	assertTier("2026-10-28", TierOther, 2, 49)
	if _, ok := got["2026-10-30/"+TierPro]; ok {
		t.Fatal("exclusive end date was included")
	}
	if len(got) != 5 {
		t.Fatalf("groups = %+v", got)
	}
}
