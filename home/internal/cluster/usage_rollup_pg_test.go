package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestUsageRollupOverviewParityPostgres seeds raw usage, then checks that the
// rollup-backed overview matches the raw path (latency percentiles excepted:
// the rollup reports histogram bucket bounds).
func TestUsageRollupOverviewParityPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLIPROXY_HOME_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLIPROXY_HOME_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("DROP SCHEMA public CASCADE")
	db.Exec("CREATE SCHEMA public")
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	username := "rollup-user"
	credits := 100.0
	user, err := repo.CreateUser(ctx, UserUpdate{Username: &username, Credits: &credits})
	if err != nil {
		t.Fatal(err)
	}
	key := "client-key-rollup-secret-0001"
	if _, err := repo.CreateAPIKeyForUser(ctx, user.ID, APIKeyUserUpdate{APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		a := &coreauth.Auth{ID: fmt.Sprintf("auth-r-%d", i), Index: fmt.Sprintf("auth-r-%d", i), Provider: "antigravity", Label: fmt.Sprintf("Cred %d", i), Status: coreauth.StatusActive}
		if _, err := repo.UpsertAuth(ctx, a, "test"); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Hour)
	models := []string{"gemini-2.5-flash", "gemini-3-flash", "gemini-3.1-pro"}
	n := 0
	for i := 0; i < 240; i++ {
		ts := base.Add(time.Duration(i) * 73 * time.Second)
		failed := i%7 == 0
		event := "completion"
		if i%31 == 0 {
			event = "probe"
		}
		apiKey := key
		if i%5 == 0 {
			apiKey = "unknown-key-xyz"
		}
		fail := ""
		if failed {
			fail = `,"failed":true,"fail":{"status_code":429,"body":"quota"}`
		}
		payload := fmt.Sprintf(`{"timestamp":%q,"provider":"antigravity","model":%q,"api_key":%q,"request_id":"req-%d","endpoint":"/v1/chat/completions","executor_type":"AntigravityExecutor","auth_index":"auth-r-%d","auth_type":"oauth","event_type":%q,"latency_ms":%d,"ttft_ms":%d,"tokens":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}%s}`,
			ts.Format(time.RFC3339Nano), models[i%3], apiKey, i, i%3, event, 200+i*37%9000, 100+i%50, 10+i, 5+i%9, 15+i+i%9, fail)
		if _, err := repo.AppendUsage(ctx, payload, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		if event != "probe" {
			n++
		}
	}
	// Raw path ignores event type; compare against a raw overview restricted the same way.
	db.Exec(`DELETE FROM "usage" WHERE event_type = 'probe'`)

	from := base.Add(-time.Hour)
	q := UsageObservabilityOverviewQuery{From: &from, Interval: "hour", Timezone: "UTC"}
	usageRollupReadyCache.Store(2)
	raw, err := repo.UsageObservabilityOverview(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MaintainUsageRollup(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v, _ := repo.UsageRollupState(ctx, usageRollupReadyKey); v == "" {
		t.Fatal("backfill not marked complete")
	}
	usageRollupReadyCache.Store(0)
	start := time.Now()
	roll, err := repo.UsageObservabilityOverview(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rollup overview took %s", time.Since(start))
	rt, ot := raw.Totals, roll.Totals
	if rt.RequestCount != int64(n) || ot.RequestCount != rt.RequestCount || ot.SuccessCount != rt.SuccessCount || ot.FailedCount != rt.FailedCount ||
		ot.InputTokens != rt.InputTokens || ot.OutputTokens != rt.OutputTokens || ot.TotalTokens != rt.TotalTokens ||
		ot.ActiveUserCount != rt.ActiveUserCount || ot.ActiveClientKeyCount != rt.ActiveClientKeyCount ||
		ot.ActiveCredentialCount != rt.ActiveCredentialCount || ot.ActiveModelCount != rt.ActiveModelCount {
		t.Fatalf("totals differ (n=%d):\nraw  %+v\nroll %+v", n, rt, ot)
	}
	if (rt.AvgLatencyMS == nil) != (ot.AvgLatencyMS == nil) || (rt.AvgLatencyMS != nil && fmt.Sprintf("%.3f", *rt.AvgLatencyMS) != fmt.Sprintf("%.3f", *ot.AvgLatencyMS)) {
		t.Fatalf("avg latency differs")
	}
	if ot.P95LatencyMS == nil || rt.P95LatencyMS == nil || *ot.P95LatencyMS < *rt.P95LatencyMS {
		t.Fatalf("p95 rollup %v should be >= raw %v", ot.P95LatencyMS, rt.P95LatencyMS)
	}
	if len(raw.Trend) != len(roll.Trend) {
		t.Fatalf("trend len %d vs %d", len(raw.Trend), len(roll.Trend))
	}
	for i := range raw.Trend {
		if raw.Trend[i].RequestCount != roll.Trend[i].RequestCount || raw.Trend[i].FailedCount != roll.Trend[i].FailedCount || !raw.Trend[i].BucketStart.Equal(roll.Trend[i].BucketStart) {
			t.Fatalf("trend[%d] raw %+v roll %+v", i, raw.Trend[i], roll.Trend[i])
		}
	}
	cmp := func(name string, a, b []UsageObservabilityAggregateItem) {
		if len(a) != len(b) {
			t.Fatalf("%s len %d vs %d", name, len(a), len(b))
		}
		ra := map[string]int64{}
		for _, x := range a {
			ra[x.ID] = x.RequestCount*1000 + x.FailedCount
		}
		for _, x := range b {
			if ra[x.ID] != x.RequestCount*1000+x.FailedCount {
				t.Fatalf("%s item %q/%q differs: raw %v roll %d/%d", name, x.ID, x.Label, ra, x.RequestCount, x.FailedCount)
			}
		}
	}
	cmp("users", raw.Top.Users, roll.Top.Users)
	cmp("keys", raw.Top.ClientKeys, roll.Top.ClientKeys)
	cmp("creds", raw.Top.Credentials, roll.Top.Credentials)
	cmp("models", raw.Top.Models, roll.Top.Models)
	cmp("providers", raw.Top.Providers, roll.Top.Providers)
	cmp("endpoints", raw.Top.Endpoints, roll.Top.Endpoints)
	cmp("errors", raw.Top.Errors, roll.Top.Errors)
	cmp("efficiency", raw.ModelEfficiency, roll.ModelEfficiency)
	usageRollupReadyCache.Store(0)
}
