package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Request-record filter options used to run six SELECT DISTINCTs over the
// full raw range (24h ≈ 700k+ rows), which timed out under load. Options are
// now built from the hourly rollup for the dimensions it keeps, plus a short
// raw window (recent rows not yet rolled up, and dimensions the rollup lacks).
// Each field gets its own short deadline; a failing field yields an empty list
// instead of failing the endpoint. Results are cached per range.

const (
	usageFilterOptionsFieldTimeout = 1500 * time.Millisecond
	usageFilterOptionsRawWindow    = time.Hour
	usageFilterOptionsRawWindowMax = 2 * time.Hour
	usageFilterOptionsCacheTTL     = 5 * time.Minute
	usageFilterOptionsPartialTTL   = 30 * time.Second
)

type usageFilterOptionsCacheEntry struct {
	options UsageObservabilityFilterOptions
	expires time.Time
}

var (
	usageFilterOptionsCacheMu sync.Mutex
	usageFilterOptionsCache   = map[string]usageFilterOptionsCacheEntry{}
)

func usageFilterOptionsCacheKey(r *Repository, query UsageObservabilityRecordQuery) string {
	q := query
	from, to := "", ""
	if q.From != nil {
		from = q.From.UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	if q.To != nil {
		to = q.To.UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	q.From, q.To, q.Limit, q.Offset = nil, nil, 0, 0
	return fmt.Sprintf("%p|%s|%s|%s", r, from, to, usageFilterOptionsQueryRest(q))
}

func usageFilterOptionsQueryRest(q UsageObservabilityRecordQuery) string {
	deref := func(p any) string {
		switch v := p.(type) {
		case *int:
			if v != nil {
				return fmt.Sprint(*v)
			}
		case *uint:
			if v != nil {
				return fmt.Sprint(*v)
			}
		case *int64:
			if v != nil {
				return fmt.Sprint(*v)
			}
		case *float64:
			if v != nil {
				return fmt.Sprint(*v)
			}
		}
		return ""
	}
	sc, uid, ckid := deref(q.StatusCode), deref(q.UserID), deref(q.ClientKeyID)
	minL, maxL, minA, maxA := deref(q.MinLatencyMS), deref(q.MaxLatencyMS), deref(q.MinAmount), deref(q.MaxAmount)
	q.StatusCode, q.UserID, q.ClientKeyID, q.MinLatencyMS, q.MaxLatencyMS, q.MinAmount, q.MaxAmount = nil, nil, nil, nil, nil, nil, nil
	return fmt.Sprintf("%+v|%s|%s|%s|%s|%s|%s|%s", q, sc, uid, ckid, minL, maxL, minA, maxA)
}

func usageFilterOptionsOnlyTimeRange(query UsageObservabilityRecordQuery) bool {
	q := query
	q.From, q.To, q.Limit, q.Offset = nil, nil, 0, 0
	return fmt.Sprintf("%+v", q) == fmt.Sprintf("%+v", UsageObservabilityRecordQuery{})
}

// usageFilterOptionsRawQuery narrows the raw-usage scan to a short window at
// the end of the requested range.
func usageFilterOptionsRawQuery(query UsageObservabilityRecordQuery, window time.Duration, now time.Time) UsageObservabilityRecordQuery {
	q := query
	end := now
	if q.To != nil && q.To.Before(end) {
		end = *q.To
	}
	start := end.Add(-window)
	if q.From != nil && q.From.After(start) {
		start = *q.From
	}
	q.From = &start
	return q
}

func (r *Repository) UsageObservabilityFilterOptions(ctx context.Context, query UsageObservabilityRecordQuery) (UsageObservabilityFilterOptions, error) {
	db, errDB := r.database()
	if errDB != nil {
		return UsageObservabilityFilterOptions{}, errDB
	}
	ctx = contextOrBackground(ctx)
	key := usageFilterOptionsCacheKey(r, query)
	now := time.Now()
	usageFilterOptionsCacheMu.Lock()
	if entry, ok := usageFilterOptionsCache[key]; ok && now.Before(entry.expires) {
		usageFilterOptionsCacheMu.Unlock()
		return entry.options, nil
	}
	usageFilterOptionsCacheMu.Unlock()

	useRollup := db.Dialector.Name() == "postgres" && usageFilterOptionsOnlyTimeRange(query) && r.usageRollupReady(ctx)
	rawQuery := query
	if db.Dialector.Name() == "postgres" {
		window := usageFilterOptionsRawWindowMax
		if useRollup {
			window = usageFilterOptionsRawWindow
		}
		rawQuery = usageFilterOptionsRawQuery(query, window, now)
	}

	failed := 0
	field := func(name string, rollupColumn string, raw func(context.Context) ([]string, error)) []string {
		fieldCtx, cancel := context.WithTimeout(ctx, usageFilterOptionsFieldTimeout)
		defer cancel()
		var merged []string
		if useRollup && rollupColumn != "" {
			values, err := usageRollupDistinctStrings(fieldCtx, db, query, rollupColumn)
			if err != nil {
				failed++
				log.WithError(err).Warnf("request-record filter options: rollup %s failed", name)
			}
			merged = append(merged, values...)
		}
		values, err := raw(fieldCtx)
		if err != nil {
			failed++
			log.WithError(err).Warnf("request-record filter options: %s failed", name)
		}
		merged = append(merged, values...)
		return mergeFilterOptionStrings(merged)
	}
	rawStrings := func(expression string) func(context.Context) ([]string, error) {
		return func(c context.Context) ([]string, error) {
			return usageObservabilityDistinctStrings(c, db, rawQuery, expression)
		}
	}

	options := UsageObservabilityFilterOptions{}
	options.EventTypes = field("event_type", "", rawStrings(`"usage"."event_type"`))
	options.Providers = field("provider", "provider", rawStrings(`"usage"."provider"`))
	options.Models = field("model", "model", rawStrings(`"usage"."model"`))
	options.HomeIPs = field("home_ip", "home_ip", rawStrings(`"usage"."home_ip"`))
	options.CPANodes = field("cpa_node", "", rawStrings(`COALESCE(NULLIF("usage"."cpa_label", ''), NULLIF("usage"."cpa_node_id", ''), NULLIF("usage"."cpa_ip", ''))`))
	codes := field("status_code", "status_code", func(c context.Context) ([]string, error) {
		ints, err := usageObservabilityDistinctStatusCodes(c, db, rawQuery)
		out := make([]string, 0, len(ints))
		for _, v := range ints {
			out = append(out, fmt.Sprint(v))
		}
		return out, err
	})
	options.StatusCodes = make([]int, 0, len(codes))
	for _, s := range codes {
		var v int
		if _, err := fmt.Sscan(s, &v); err == nil && v > 0 {
			options.StatusCodes = append(options.StatusCodes, v)
		}
	}
	sort.Ints(options.StatusCodes)

	if ctx.Err() == nil {
		ttl := usageFilterOptionsCacheTTL
		if failed > 0 {
			ttl = usageFilterOptionsPartialTTL
		}
		usageFilterOptionsCacheMu.Lock()
		for k, e := range usageFilterOptionsCache {
			if now.After(e.expires) {
				delete(usageFilterOptionsCache, k)
			}
		}
		usageFilterOptionsCache[key] = usageFilterOptionsCacheEntry{options: options, expires: now.Add(ttl)}
		usageFilterOptionsCacheMu.Unlock()
	}
	return options, nil
}

func usageRollupDistinctStrings(ctx context.Context, db *gorm.DB, query UsageObservabilityRecordQuery, column string) ([]string, error) {
	scope := db.WithContext(ctx).Table(usageRollupTable)
	if query.From != nil {
		scope = scope.Where(`"timestamp" >= ?`, query.From.UTC().Truncate(time.Hour))
	}
	if query.To != nil {
		scope = scope.Where(`"timestamp" <= ?`, query.To.UTC())
	}
	var values []string
	expr := `"` + column + `"`
	if column == "status_code" {
		scope = scope.Where(expr + ` > 0`)
		expr = `CAST(` + expr + ` AS TEXT)`
	} else {
		scope = scope.Where(`TRIM(` + expr + `) <> ''`)
	}
	type valueRow struct {
		Value string `gorm:"column:value"`
	}
	var rows []valueRow
	if err := scope.Select(`DISTINCT ` + expr + ` AS value`).Limit(500).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		values = append(values, row.Value)
	}
	return values, nil
}

func mergeFilterOptionStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
