package cluster

import (
	"context"
	"testing"
	"time"
)

func TestUsageFilterOptionsRawQueryClampsWindow(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)
	q := usageFilterOptionsRawQuery(UsageObservabilityRecordQuery{From: &from}, time.Hour, now)
	if q.From == nil || !q.From.Equal(now.Add(-time.Hour)) {
		t.Fatalf("raw from = %v, want %v", q.From, now.Add(-time.Hour))
	}
	shortFrom := now.Add(-10 * time.Minute)
	q = usageFilterOptionsRawQuery(UsageObservabilityRecordQuery{From: &shortFrom}, time.Hour, now)
	if !q.From.Equal(shortFrom) {
		t.Fatalf("short range should keep from, got %v", q.From)
	}
	to := now.Add(-5 * time.Hour)
	q = usageFilterOptionsRawQuery(UsageObservabilityRecordQuery{From: &from, To: &to}, time.Hour, now)
	if !q.From.Equal(to.Add(-time.Hour)) {
		t.Fatalf("historical range raw from = %v, want %v", q.From, to.Add(-time.Hour))
	}
}

func TestUsageFilterOptionsOnlyTimeRange(t *testing.T) {
	from := time.Now()
	if !usageFilterOptionsOnlyTimeRange(UsageObservabilityRecordQuery{From: &from, Limit: 10}) {
		t.Fatal("time-only query should be rollup eligible")
	}
	if usageFilterOptionsOnlyTimeRange(UsageObservabilityRecordQuery{From: &from, EventType: "x"}) {
		t.Fatal("filtered query must not use rollup")
	}
}

func TestMergeFilterOptionStrings(t *testing.T) {
	got := mergeFilterOptionStrings([]string{"b", " a ", "B", "", "a"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("merge = %v", got)
	}
}

func TestUsageFilterOptionsFailedFieldReturnsEmptyNotError(t *testing.T) {
	repo := newCredentialFoundationTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every field query fails
	options, err := repo.UsageObservabilityFilterOptions(ctx, UsageObservabilityRecordQuery{})
	if err != nil {
		t.Fatalf("UsageObservabilityFilterOptions() error = %v, want nil", err)
	}
	if len(options.Models) != 0 || len(options.StatusCodes) != 0 {
		t.Fatalf("expected empty options, got %+v", options)
	}
	// A canceled request must not poison the cache.
	if _, ok := usageFilterOptionsCache[usageFilterOptionsCacheKey(repo, UsageObservabilityRecordQuery{})]; ok {
		t.Fatal("failed request was cached")
	}
}
