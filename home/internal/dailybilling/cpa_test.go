package dailybilling

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openBillingFixture(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}

func TestCPAQueryAggregatesByDayAndTier(t *testing.T) {
	t.Parallel()

	db := openBillingFixture(t)
	query, errRange := ResolveRange(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), "2026-10-27", "2026-10-28", "UTC")
	if errRange != nil {
		t.Fatal(errRange)
	}
	// The prefix filter is opt-in. This fixture keeps the old gemini-4 boundary.
	cfg := Config{ModelPrefix: "gemini-4-argon", ModelExclude: "-disabled"}.normalized()
	rows, errQuery := queryCPATiers(context.Background(), db, cfg, query)
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

func TestCPAQueryDefaultsToAllModels(t *testing.T) {
	t.Parallel()

	db := openBillingFixture(t)
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
	// gemini-3-flash and the disabled variant are included. Distinct auth_index is the active count.
	if row := got["2026-10-27/"+TierPro]; row.Requests != 2 || row.TotalTokens != 5700 || row.ActiveCredentials != 1 {
		t.Fatalf("day1 pro = %+v", row)
	}
	if row := got["2026-10-27/"+TierUltra]; row.Requests != 1 || row.TotalTokens != 300 || row.ActiveCredentials != 1 {
		t.Fatalf("day1 ultra = %+v", row)
	}
	if row := got["2026-10-28/"+TierPro]; row.Requests != 2 || row.TotalTokens != 150 || row.ActiveCredentials != 1 {
		t.Fatalf("day2 pro = %+v", row)
	}
	if row := got["2026-10-28/"+TierOther]; row.Requests != 2 || row.TotalTokens != 49 || row.ActiveCredentials != 2 {
		t.Fatalf("day2 other = %+v", row)
	}
}

func TestModelPredicateAndChannelClause(t *testing.T) {
	t.Parallel()

	sqlText, args := modelPredicate("model_name", Config{}.normalized())
	if sqlText != "TRUE" || len(args) != 0 {
		t.Fatalf("default predicate = %s %v", sqlText, args)
	}
	clause, channelArgs := channelClause(Config{}.normalized())
	if clause != "" || len(channelArgs) != 0 {
		t.Fatalf("default channel = %s %v", clause, channelArgs)
	}
	clause, channelArgs = channelClause(Config{ChannelIDs: []int{60, 59, 59, 0}}.normalized())
	if clause != "AND channel_id IN (?,?)" || len(channelArgs) != 2 || channelArgs[0] != 59 || channelArgs[1] != 60 {
		t.Fatalf("channel list = %s %v", clause, channelArgs)
	}
	sqlText, args = modelPredicate("u.model", Config{Models: []string{"gemini-4-argon", "gemini-3-flash"}, ModelExclude: "-disabled"}.normalized())
	if !strings.Contains(sqlText, "IN (?,?)") || !strings.Contains(sqlText, "NOT LIKE") || len(args) != 3 {
		t.Fatalf("model list = %s %v", sqlText, args)
	}
}
