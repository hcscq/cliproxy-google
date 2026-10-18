package cluster

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// usage_rollup_hourly pre-aggregates the usage table per hour and per
// dimension (provider, model, endpoint, home, credential, client key/user,
// status) so the panel overview never scans raw rows. Probe and health-check
// events are excluded (they are not client traffic). Latency percentiles come
// from a fixed histogram (upper bucket bound, so they are approximate).

const usageRollupTable = "usage_rollup_hourly"

// usageRollupLatencyBounds are histogram upper bounds in ms; the last bucket is open.
var usageRollupLatencyBounds = []int64{100, 250, 500, 750, 1000, 1500, 2000, 3000, 4000, 5000, 6000, 8000, 10000, 12500, 15000, 20000, 25000, 30000, 45000, 60000, 90000, 120000, 180000, 300000}

func usageRollupHistColumns() []string {
	cols := make([]string, len(usageRollupLatencyBounds)+1)
	for i := range cols {
		cols[i] = fmt.Sprintf("h%02d", i)
	}
	return cols
}

var usageRollupMeasureColumns = []string{
	"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "cache_read_tokens", "cache_creation_tokens",
	"total_tokens", "accounting_total_tokens", "accounting_input_tokens", "uncached_input_tokens",
	"accounting_cache_read_tokens", "accounting_cache_write_tokens", "accounting_output_tokens",
	"non_reasoning_output_tokens", "accounting_reasoning_tokens", "unclassified_tokens", "cache_rate_tokens",
}

func ensureUsageRollupTables(ctx context.Context, db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	cols := []string{
		`"timestamp" timestamptz NOT NULL`,
		`"provider" text NOT NULL DEFAULT ''`, `"model" text NOT NULL DEFAULT ''`, `"endpoint" text NOT NULL DEFAULT ''`,
		`"home_ip" text NOT NULL DEFAULT ''`, `"auth_type" text NOT NULL DEFAULT ''`, `"auth_index" text NOT NULL DEFAULT ''`,
		`"user_id" bigint`, `"api_key_id" bigint`, `"api_key_label" text`, `"api_key_masked" text`,
		`"failed" boolean NOT NULL DEFAULT false`, `"retried_ok" boolean NOT NULL DEFAULT false`, `"status_code" bigint NOT NULL DEFAULT 0`,
		`"request_count" bigint NOT NULL DEFAULT 0`, `"failed_count" bigint NOT NULL DEFAULT 0`, `"retried_ok_count" bigint NOT NULL DEFAULT 0`,
		`"inconsistent_count" bigint NOT NULL DEFAULT 0`, `"unclassified_count" bigint NOT NULL DEFAULT 0`,
		`"amount" numeric`, `"latency_sum" bigint NOT NULL DEFAULT 0`, `"latency_count" bigint NOT NULL DEFAULT 0`,
		`"ttft_sum" bigint NOT NULL DEFAULT 0`, `"ttft_count" bigint NOT NULL DEFAULT 0`,
		`"min_ts" timestamptz`, `"max_ts" timestamptz`,
	}
	for _, m := range usageRollupMeasureColumns {
		cols = append(cols, fmt.Sprintf(`"%s" bigint NOT NULL DEFAULT 0`, m))
	}
	for _, h := range usageRollupHistColumns() {
		cols = append(cols, fmt.Sprintf(`"%s" bigint NOT NULL DEFAULT 0`, h))
	}
	tx := db.WithContext(contextOrBackground(ctx))
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS "` + usageRollupTable + `" (` + strings.Join(cols, ", ") + `)`,
		`CREATE INDEX IF NOT EXISTS "idx_usage_rollup_hourly_ts" ON "` + usageRollupTable + `" ("timestamp")`,
		`CREATE TABLE IF NOT EXISTS "usage_rollup_state" ("key" text PRIMARY KEY, "value" text NOT NULL, "updated_at" timestamptz NOT NULL DEFAULT now())`,
	} {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// usageRollupInsertSQL aggregates raw usage in [$1,$2) into the rollup.
func usageRollupInsertSQL() string {
	dims := []string{
		`date_trunc('hour', "usage"."timestamp")`,
		`COALESCE("usage"."provider", '')`, `COALESCE("usage"."model", '')`, `COALESCE("usage"."endpoint", '')`,
		`COALESCE("usage"."home_ip", '')`, `COALESCE("usage"."auth_type", '')`, `COALESCE("usage"."auth_index", '')`,
		`COALESCE("billing_charge"."user_id", "api_key"."user_id")`, `COALESCE("billing_charge"."api_key_id", "api_key"."id")`,
		`"usage"."failed"`, `"usage"."retried_ok"`, usageObservabilityEffectiveStatusCodeSQL(),
	}
	sel := append([]string{}, dims...)
	sel = append(sel,
		`MAX(COALESCE("billing_charge"."api_key_label", CASE WHEN "api_key"."id" IS NULL THEN '' ELSE 'api-key-' || CAST("api_key"."id" AS TEXT) END))`,
		`MAX("billing_charge"."api_key_masked")`,
		`COUNT(*)`,
		`SUM(CASE WHEN "usage"."failed" THEN 1 ELSE 0 END)`,
		`SUM(CASE WHEN "usage"."failed" AND "usage"."retried_ok" THEN 1 ELSE 0 END)`,
		`SUM(CASE WHEN "usage"."token_accounting_quality" = 'inconsistent' THEN 1 ELSE 0 END)`,
		`SUM(CASE WHEN "usage"."token_accounting_quality" = 'unclassified' AND "usage"."unclassified_tokens" > 0 THEN 1 ELSE 0 END)`,
		`SUM("billing_charge"."amount")`,
		`COALESCE(SUM(CASE WHEN "usage"."latency_ms" >= 0 THEN "usage"."latency_ms" END), 0)`,
		`SUM(CASE WHEN "usage"."latency_ms" >= 0 THEN 1 ELSE 0 END)`,
		`COALESCE(SUM(CASE WHEN "usage"."ttft_ms" > 0 THEN "usage"."ttft_ms" END), 0)`,
		`SUM(CASE WHEN "usage"."ttft_ms" > 0 THEN 1 ELSE 0 END)`,
		`MIN("usage"."timestamp")`, `MAX("usage"."timestamp")`,
	)
	measureExpr := map[string]string{
		"cache_read_tokens":       usageObservabilitySQLCacheReadTokens(`"usage"`),
		"accounting_total_tokens": usageObservabilitySQLAccountingTotalTokens(`"usage"`),
		"cache_rate_tokens":       usageObservabilitySQLCacheRateTokens(`"usage"`),
	}
	for _, m := range usageRollupMeasureColumns {
		e, ok := measureExpr[m]
		if !ok {
			e = `"usage"."` + m + `"`
		}
		sel = append(sel, `COALESCE(SUM(`+e+`), 0)`)
	}
	bucketExpr := fmt.Sprintf(`width_bucket("usage"."latency_ms", ARRAY[%s]::bigint[])`, joinInt64s(usageRollupLatencyBounds))
	for i := range usageRollupHistColumns() {
		sel = append(sel, fmt.Sprintf(`SUM(CASE WHEN "usage"."latency_ms" >= 0 AND %s = %d THEN 1 ELSE 0 END)`, bucketExpr, i))
	}
	target := []string{`"timestamp"`, `"provider"`, `"model"`, `"endpoint"`, `"home_ip"`, `"auth_type"`, `"auth_index"`, `"user_id"`, `"api_key_id"`,
		`"failed"`, `"retried_ok"`, `"status_code"`, `"api_key_label"`, `"api_key_masked"`, `"request_count"`, `"failed_count"`, `"retried_ok_count"`,
		`"inconsistent_count"`, `"unclassified_count"`, `"amount"`, `"latency_sum"`, `"latency_count"`, `"ttft_sum"`, `"ttft_count"`, `"min_ts"`, `"max_ts"`}
	for _, m := range usageRollupMeasureColumns {
		target = append(target, `"`+m+`"`)
	}
	for _, h := range usageRollupHistColumns() {
		target = append(target, `"`+h+`"`)
	}
	groupBy := make([]string, len(dims))
	for i := range dims {
		groupBy[i] = fmt.Sprint(i + 1)
	}
	return `INSERT INTO "` + usageRollupTable + `" (` + strings.Join(target, ", ") + `) SELECT ` + strings.Join(sel, ", ") +
		` FROM "usage" LEFT JOIN "billing_charge" ON "billing_charge"."usage_id" = "usage"."id" LEFT JOIN "api_key" ON "api_key"."api_key" = "usage"."api_key"` +
		` WHERE "usage"."timestamp" >= ? AND "usage"."timestamp" < ? AND COALESCE("usage"."event_type", '') NOT IN ('probe', 'health_check')` +
		` GROUP BY ` + strings.Join(groupBy, ", ")
}

func joinInt64s(v []int64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ",")
}

// RefreshUsageRollup recomputes the rollup for whole hours covering [from, to).
func (r *Repository) RefreshUsageRollup(ctx context.Context, from, to time.Time) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	from = from.UTC().Truncate(time.Hour)
	to = to.UTC().Truncate(time.Hour).Add(time.Hour)
	return db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL statement_timeout = '300s'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM "`+usageRollupTable+`" WHERE "timestamp" >= ? AND "timestamp" < ?`, from, to).Error; err != nil {
			return err
		}
		return tx.Exec(usageRollupInsertSQL(), from, to).Error
	})
}

// UsageRollupState reads a rollup state key ("" when unset).
func (r *Repository) UsageRollupState(ctx context.Context, key string) (string, error) {
	db, err := r.database()
	if err != nil {
		return "", err
	}
	if db.Dialector.Name() != "postgres" {
		return "", nil
	}
	var v []string
	if err := db.WithContext(contextOrBackground(ctx)).Raw(`SELECT "value" FROM "usage_rollup_state" WHERE "key" = ?`, key).Scan(&v).Error; err != nil {
		return "", err
	}
	if len(v) == 0 {
		return "", nil
	}
	return v[0], nil
}

// SetUsageRollupState upserts a rollup state key.
func (r *Repository) SetUsageRollupState(ctx context.Context, key, value string) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if key == usageRollupReadyKey {
		usageRollupReadyCache.Store(0)
	}
	return db.WithContext(contextOrBackground(ctx)).Exec(`INSERT INTO "usage_rollup_state" ("key", "value", "updated_at") VALUES (?, ?, now()) ON CONFLICT ("key") DO UPDATE SET "value" = EXCLUDED."value", "updated_at" = now()`, key, value).Error
}

// UsageRawMinTimestamp returns the oldest raw usage timestamp (zero if none).
func (r *Repository) UsageRawMinTimestamp(ctx context.Context) (time.Time, error) {
	db, err := r.database()
	if err != nil {
		return time.Time{}, err
	}
	var v []time.Time
	if err := db.WithContext(contextOrBackground(ctx)).Raw(`SELECT MIN("timestamp") FROM "usage" WHERE "timestamp" IS NOT NULL HAVING MIN("timestamp") IS NOT NULL`).Scan(&v).Error; err != nil {
		return time.Time{}, err
	}
	if len(v) == 0 {
		return time.Time{}, nil
	}
	return v[0], nil
}

const usageRollupReadyKey = "backfill_complete"

// usageRollupReadyCache: 0 unknown, 1 ready, 2 not ready (re-checked every 30s).
var (
	usageRollupReadyCache   atomic.Int32
	usageRollupReadyChecked atomic.Int64
)

func (r *Repository) usageRollupReady(ctx context.Context) bool {
	now := time.Now().Unix()
	if v := usageRollupReadyCache.Load(); v != 0 && now-usageRollupReadyChecked.Load() < 30 {
		return v == 1
	}
	v, err := r.UsageRollupState(ctx, usageRollupReadyKey)
	state := int32(2)
	if err == nil && v != "" {
		state = 1
	}
	usageRollupReadyCache.Store(state)
	usageRollupReadyChecked.Store(now)
	return state == 1
}

// usageRollupEligible reports whether the overview query only uses
// dimensions the rollup keeps.
func usageRollupEligible(query UsageObservabilityRecordQuery, interval string) bool {
	if interval == "minute" {
		return false
	}
	q := query
	q.From, q.To, q.Provider, q.Model, q.HomeIP, q.Endpoint, q.CredentialType, q.Status = nil, nil, "", "", "", "", "", ""
	q.Limit, q.Offset = 0, 0
	return fmt.Sprintf("%+v", q) == fmt.Sprintf("%+v", UsageObservabilityRecordQuery{})
}

// usageRollupScope selects the rollup aliased as "usage" with the overview
// filters applied (From floored to the hour).
func usageRollupScope(db *gorm.DB, query UsageObservabilityRecordQuery) *gorm.DB {
	q := query
	if q.From != nil {
		f := q.From.UTC().Truncate(time.Hour)
		q.From = &f
	}
	return usageObservabilityApplyUsageFilters(db.Table(`"`+usageRollupTable+`" AS "usage"`), q)
}

// usageRollupPercentileSQL picks the histogram bucket upper bound at pct.
func usageRollupPercentileSQL(pct int) string {
	hs := usageRollupHistColumns()
	total := make([]string, len(hs))
	for i, h := range hs {
		total[i] = `SUM("usage"."` + h + `")`
	}
	totalExpr := `(` + strings.Join(total, " + ") + `)`
	var b strings.Builder
	b.WriteString(`CASE WHEN ` + totalExpr + ` = 0 THEN NULL`)
	for i := range usageRollupLatencyBounds {
		b.WriteString(fmt.Sprintf(` WHEN (%s) * 100 >= %s * %d THEN %d`, strings.Join(total[:i+1], " + "), totalExpr, pct, usageRollupLatencyBounds[i]))
	}
	b.WriteString(fmt.Sprintf(` ELSE %d END`, usageRollupLatencyBounds[len(usageRollupLatencyBounds)-1]))
	return b.String()
}

func usageRollupSumSelect() string {
	parts := []string{
		`SUM("usage"."request_count") AS request_count`,
		`SUM("usage"."request_count") - SUM("usage"."failed_count") AS success_count`,
		`SUM("usage"."failed_count") AS failed_count`,
		`SUM("usage"."retried_ok_count") AS retried_ok_count`,
	}
	for _, m := range usageRollupMeasureColumns {
		if m == "cache_rate_tokens" {
			continue
		}
		parts = append(parts, fmt.Sprintf(`SUM("usage"."%s") AS %s`, m, m))
	}
	parts = append(parts,
		`CASE WHEN SUM("usage"."inconsistent_count") > 0 THEN 'inconsistent' WHEN SUM("usage"."unclassified_count") > 0 THEN 'unclassified' ELSE 'complete' END AS token_accounting_quality`,
		`SUM("usage"."amount") AS total_amount`,
		`CAST(SUM("usage"."latency_sum") AS double precision) / NULLIF(SUM("usage"."latency_count"), 0) AS avg_latency_ms`,
	)
	return strings.Join(parts, ",\n")
}

func (r *Repository) usageRollupOverview(ctx context.Context, db *gorm.DB, query UsageObservabilityOverviewQuery) (UsageObservabilityOverview, error) {
	recordQuery := usageObservabilityOverviewRecordQuery(query)
	var row usageObservabilityTotalsRow
	var pct struct {
		P50 *float64 `gorm:"column:p50"`
		P95 *float64 `gorm:"column:p95"`
	}
	totalsSelect := usageRollupSumSelect() + `,
		CAST(SUM("usage"."ttft_sum") AS double precision) / NULLIF(SUM("usage"."ttft_count"), 0) AS avg_ttft_ms,
		COUNT(DISTINCT "usage"."user_id") AS active_user_count,
		COUNT(DISTINCT "usage"."api_key_id") AS active_client_key_count,
		COUNT(DISTINCT NULLIF("usage"."auth_index", '')) AS active_credential_count,
		COUNT(DISTINCT NULLIF("usage"."model", '')) AS active_model_count,
		MIN("usage"."min_ts") AS min_timestamp,
		MAX("usage"."max_ts") AS max_timestamp`
	if err := usageRollupScope(db, recordQuery).Select(totalsSelect).Scan(&row).Error; err != nil {
		return UsageObservabilityOverview{}, err
	}
	if err := usageRollupScope(db, recordQuery).Select(usageRollupPercentileSQL(50) + ` AS p50, ` + usageRollupPercentileSQL(95) + ` AS p95`).Scan(&pct).Error; err != nil {
		return UsageObservabilityOverview{}, err
	}
	totals := usageObservabilityTotalsFromRow(&row)
	totals.P50LatencyMS, totals.P95LatencyMS = pct.P50, pct.P95
	bounds := usageObservabilityOverviewBounds{MinTimestamp: row.MinTimestamp, MaxTimestamp: row.MaxTimestamp}

	location := usageObservabilityLocation(query.Timezone)
	interval, errInterval := usageObservabilityOverviewIntervalForBounds(query.Interval, location, query.From, query.To, bounds)
	if errInterval != nil {
		return UsageObservabilityOverview{}, errInterval
	}
	overview := UsageObservabilityOverview{
		Range: UsageObservabilityOverviewRange{
			From:     usageObservabilityRangeTimeFromBounds(query.From, bounds, true),
			To:       usageObservabilityRangeTimeFromBounds(query.To, bounds, false),
			Timezone: firstNonEmptyUsageObservabilityString(query.Timezone, "UTC"),
			Interval: interval,
		},
		Totals:          totals,
		CostBreakdown:   []UsageObservabilityCostBreakdownItem{},
		ModelEfficiency: []UsageObservabilityAggregateItem{},
	}
	live, errLive := usageObservabilityLiveSQL(db, recordQuery, 300)
	if errLive != nil {
		return UsageObservabilityOverview{}, errLive
	}
	overview.Live = live

	bucketExpr, bucketArgs := usageObservabilityTrendBucketUnixSQL(db, interval, location, recordQuery, bounds)
	var trendRows []usageObservabilityTrendBucketRow
	if err := usageRollupScope(db, recordQuery).
		Select(bucketExpr+` AS bucket_unix, `+usageRollupSumSelect(), bucketArgs...).
		Group("bucket_unix").Order("bucket_unix ASC").Scan(&trendRows).Error; err != nil {
		return UsageObservabilityOverview{}, err
	}
	var p95Rows []usageObservabilityTrendPercentileRow
	if err := usageRollupScope(db, recordQuery).
		Select(bucketExpr+` AS bucket_unix, `+usageRollupPercentileSQL(95)+` AS p95_latency_ms`, bucketArgs...).
		Group("bucket_unix").Scan(&p95Rows).Error; err != nil {
		return UsageObservabilityOverview{}, err
	}
	p95 := make(map[int64]sql.NullFloat64, len(p95Rows))
	for i := range p95Rows {
		p95[p95Rows[i].BucketUnix] = p95Rows[i].P95LatencyMS
	}
	trend := usageObservabilityTrendFromBucketRows(trendRows, p95, interval, location)
	var errTrend error
	overview.Trend, errTrend = usageObservabilityFillTrend(trend, interval, location, query.From, query.To, bounds)
	if errTrend != nil {
		return UsageObservabilityOverview{}, errTrend
	}
	overview.Activity = usageObservabilityActivity(overview.Trend)

	agg := func(q UsageObservabilityRecordQuery, groupBy, metric string) ([]UsageObservabilityAggregateItem, error) {
		return usageRollupAggregateItems(db, q, groupBy, metric, 10)
	}
	var err error
	if overview.ModelEfficiency, err = agg(recordQuery, "model", "total_tokens"); err != nil {
		return UsageObservabilityOverview{}, err
	}
	top := UsageObservabilityTopGroups{}
	for _, g := range []struct {
		name string
		dst  *[]UsageObservabilityAggregateItem
	}{{"user", &top.Users}, {"client_key", &top.ClientKeys}, {"credential", &top.Credentials}, {"provider", &top.Providers}, {"model", &top.Models}, {"endpoint", &top.Endpoints}} {
		if *g.dst, err = agg(recordQuery, g.name, "request_count"); err != nil {
			return UsageObservabilityOverview{}, err
		}
	}
	errQuery := recordQuery
	errQuery.Status = "failed"
	if top.Errors, err = agg(errQuery, "status_code", "failed_count"); err != nil {
		return UsageObservabilityOverview{}, err
	}
	overview.Top = top
	return overview, nil
}

func usageRollupAggregateItems(db *gorm.DB, query UsageObservabilityRecordQuery, groupBy, metric string, limit int) ([]UsageObservabilityAggregateItem, error) {
	scope := usageRollupScope(db, query)
	idExpr, labelExpr := "", ""
	meta := map[string]string{}
	switch groupBy {
	case "user":
		scope = scope.Joins(`LEFT JOIN "user" ON "user"."id" = "usage"."user_id"`)
		idExpr = `COALESCE(CAST("usage"."user_id" AS TEXT), 'unknown')`
		labelExpr = `COALESCE(NULLIF(MAX("user"."username"), ''), ` + idExpr + `)`
		meta["metadata_user_id"] = `MAX("usage"."user_id")`
		meta["metadata_username"] = `MAX("user"."username")`
	case "client_key":
		idExpr = `COALESCE(CAST("usage"."api_key_id" AS TEXT), 'unknown')`
		labelExpr = `COALESCE(NULLIF(MAX("usage"."api_key_label"), ''), NULLIF(MAX("usage"."api_key_masked"), ''), ` + idExpr + `)`
		meta["metadata_user_id"] = `MAX("usage"."user_id")`
		meta["metadata_api_key_id"] = `MAX("usage"."api_key_id")`
		meta["metadata_api_key_label"] = `MAX("usage"."api_key_label")`
		meta["metadata_api_key_masked"] = `MAX("usage"."api_key_masked")`
	case "credential":
		scope = scope.
			Joins(`LEFT JOIN "auth" AS "auth_uuid" ON "auth_uuid"."deleted_at" IS NULL AND "auth_uuid"."uuid" = "usage"."auth_index"`).
			Joins(`LEFT JOIN "auth" AS "auth_by_index" ON "auth_uuid"."uuid" IS NULL AND "auth_by_index"."deleted_at" IS NULL AND "auth_by_index"."index" = "usage"."auth_index"`).
			Joins(`LEFT JOIN "auth" AS "auth_by_id" ON "auth_uuid"."uuid" IS NULL AND "auth_by_index"."uuid" IS NULL AND "auth_by_id"."deleted_at" IS NULL AND "auth_by_id"."id" = "usage"."auth_index"`)
		idExpr, labelExpr = usageObservabilityAggregateSQLIdentity("credential")
		labelExpr = `MAX(` + labelExpr + `)`
		meta["metadata_credential_type"] = `MAX("usage"."auth_type")`
		meta["metadata_credential_id"] = `MAX(` + usageObservabilitySQLCredentialID() + `)`
		meta["metadata_auth_index"] = `MAX(` + usageObservabilitySQLAuthIndex() + `)`
		meta["metadata_credential_provider"] = `MAX(` + usageObservabilitySQLAuthProvider() + `)`
		meta["metadata_credential_label"] = `MAX(` + usageObservabilitySQLAuthLabel() + `)`
		meta["metadata_auth_uuid"] = `MAX(` + usageObservabilitySQLAuthUUID() + `)`
		meta["metadata_auth_next_retry_at"] = `MAX(` + usageObservabilitySQLAuthNextRetryAfter() + `)`
		meta["metadata_auth_status"] = `MAX(` + usageObservabilitySQLAuthStatus() + `)`
		meta["metadata_auth_disabled"] = `MAX(CASE WHEN ` + usageObservabilitySQLAuthDisabled() + ` THEN 1 ELSE 0 END)`
		meta["metadata_auth_unavailable"] = `MAX(CASE WHEN ` + usageObservabilitySQLAuthUnavailable() + ` THEN 1 ELSE 0 END)`
	case "status_code":
		idExpr = `CAST("usage"."status_code" AS TEXT)`
		labelExpr = idExpr
	case "model":
		idExpr = `COALESCE(NULLIF("usage"."model", ''), 'unknown')`
		labelExpr = idExpr
	case "endpoint":
		idExpr = `COALESCE(NULLIF("usage"."endpoint", ''), 'unknown')`
		labelExpr = idExpr
	default:
		idExpr = `COALESCE(NULLIF("usage"."provider", ''), 'unknown')`
		labelExpr = idExpr
	}
	sel := []string{idExpr + ` AS aggregate_id`, labelExpr + ` AS aggregate_label`}
	for _, k := range []string{"metadata_user_id", "metadata_username", "metadata_api_key_id", "metadata_api_key_label", "metadata_api_key_masked",
		"metadata_credential_type", "metadata_credential_id", "metadata_auth_index", "metadata_credential_provider", "metadata_credential_label",
		"metadata_auth_uuid", "metadata_auth_next_retry_at", "metadata_auth_status", "metadata_auth_disabled", "metadata_auth_unavailable"} {
		if e, ok := meta[k]; ok {
			sel = append(sel, e+` AS `+k)
		}
	}
	sel = append(sel,
		`MAX("usage"."provider") AS metadata_provider`,
		`CASE WHEN SUM("usage"."request_count") = SUM("usage"."failed_count") THEN 'failed' ELSE 'success' END AS metadata_status`,
		usageRollupSumSelect(),
		`SUM("usage"."cache_rate_tokens") AS cache_rate_tokens`,
		`SUM("usage"."accounting_total_tokens") AS cache_rate_total_tokens`,
		usageRollupPercentileSQL(95)+` AS p95_latency_ms`,
		`MAX("usage"."max_ts") AS last_used_at`,
	)
	order := "request_count DESC"
	switch metric {
	case "total_tokens":
		order = "accounting_total_tokens DESC"
	case "failed_count":
		order = "failed_count DESC"
	}
	var rows []usageObservabilityAggregateRow
	if err := scope.Select(strings.Join(sel, ",\n")).Group(idExpr).Order(order + ", aggregate_id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]UsageObservabilityAggregateItem, 0, len(rows))
	for i := range rows {
		items = append(items, usageObservabilityAggregateItemFromRow(&rows[i], groupBy))
	}
	return items, nil
}

const (
	usageRollupCursorKey     = "backfill_cursor"
	usageRollupRefreshWindow = 2 * time.Hour
	usageRollupBackfillChunk = 6 * time.Hour
	usageRollupTickBudget    = 20 * time.Second
)

// MaintainUsageRollup re-aggregates the recent window and walks the backfill
// cursor backwards in bounded chunks. Leader-only; idempotent (DELETE+INSERT
// per hour range), so overlap or a crashed tick is harmless.
func (r *Repository) MaintainUsageRollup(ctx context.Context, now time.Time) error {
	if r == nil || r.db == nil || r.db.Dialector.Name() != "postgres" {
		return nil
	}
	start := time.Now()
	recentFrom := now.UTC().Add(-usageRollupRefreshWindow).Truncate(time.Hour)
	if err := r.RefreshUsageRollup(ctx, recentFrom, now.UTC().Add(time.Hour)); err != nil {
		return fmt.Errorf("refresh recent usage rollup: %w", err)
	}
	if done, _ := r.UsageRollupState(ctx, usageRollupReadyKey); done != "" {
		return nil
	}
	minTS, err := r.UsageRawMinTimestamp(ctx)
	if err != nil {
		return err
	}
	cursor := recentFrom
	if v, _ := r.UsageRollupState(ctx, usageRollupCursorKey); v != "" {
		if t, errParse := time.Parse(time.RFC3339, v); errParse == nil && t.Before(cursor) {
			cursor = t
		}
	}
	floor := minTS.UTC().Truncate(time.Hour)
	for !minTS.IsZero() && cursor.After(floor) && time.Since(start) < usageRollupTickBudget {
		from := cursor.Add(-usageRollupBackfillChunk)
		if from.Before(floor) {
			from = floor
		}
		if err := r.RefreshUsageRollup(ctx, from, cursor); err != nil {
			return fmt.Errorf("backfill usage rollup %s..%s: %w", from.Format(time.RFC3339), cursor.Format(time.RFC3339), err)
		}
		cursor = from
		if err := r.SetUsageRollupState(ctx, usageRollupCursorKey, cursor.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if minTS.IsZero() || !cursor.After(floor) {
		return r.SetUsageRollupState(ctx, usageRollupReadyKey, now.UTC().Format(time.RFC3339))
	}
	return nil
}
