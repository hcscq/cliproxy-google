package dailybilling

import (
	"context"
	"errors"
	"testing"
)

type fakeLogs struct {
	rows []LogRow
	err  error
}

func (f fakeLogs) Aggregate(context.Context, Config, Query) ([]LogRow, error) {
	return f.rows, f.err
}

type fakeTiers struct {
	rows []TierRow
	err  error
}

func (f fakeTiers) Aggregate(context.Context, Config, Query) ([]TierRow, error) {
	return f.rows, f.err
}

func TestReportUnconfiguredDoesNotQuery(t *testing.T) {
	t.Parallel()
	svc := NewService(Config{}, nil)
	if svc.DefaultTimezone() != DefaultTimezone {
		t.Fatal(svc.DefaultTimezone())
	}
	report, errReport := svc.Report(context.Background(), Query{})
	if errReport != nil {
		t.Fatal(errReport)
	}
	if report.Configured || report.Message != UnconfiguredMessage {
		t.Fatalf("report = %+v", report)
	}
	if report.Estimated || report.Discrepancy != nil {
		t.Fatalf("unconfigured report should not look like data: %+v", report)
	}
}

func TestReportAllocatesFixtureDays(t *testing.T) {
	t.Parallel()
	cfg := Config{Host: "db.internal", Database: "newapi", Password: "do-not-log"}.normalized()
	svc := NewService(cfg, nil)
	svc.logs = fakeLogs{rows: []LogRow{
		{Day: "2026-10-27", Model: "gemini-4-argon", Requests: 10, PromptTokens: 100, CompletionTokens: 20, Quota: 500000},
		{Day: "2026-10-28", Model: "gemini-4-argon-high", Requests: 2, PromptTokens: 10, CompletionTokens: 4, Quota: 0},
	}}
	svc.tiers = fakeTiers{rows: []TierRow{
		{Day: "2026-10-27", Tier: TierPro, Requests: 4, TotalTokens: 80},
		{Day: "2026-10-27", Tier: TierUltra, Requests: 1, TotalTokens: 20},
		{Day: "2026-10-28", Tier: TierUltra, Requests: 1, TotalTokens: 5},
	}}
	report, errReport := svc.Report(context.Background(), Query{FromDate: "2026-10-27", ToDate: "2026-10-28", Timezone: "UTC"})
	if errReport != nil {
		t.Fatal(errReport)
	}
	if !report.Configured || !report.Estimated {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Days) != 2 {
		t.Fatalf("days = %d", len(report.Days))
	}
	if report.Days[0].Rows[0].Tier != TierPro || report.Days[0].Rows[0].Quota != 400000 {
		t.Fatalf("day1 pro = %+v", report.Days[0].Rows[0])
	}
	if report.Days[1].Unallocated || len(report.Days[1].Rows) != 1 || report.Days[1].Rows[0].Tier != TierUltra || report.Days[1].Rows[0].Requests != 2 {
		t.Fatalf("day2 = %+v", report.Days[1])
	}
	if report.Totals.USD != 1 || report.Totals.ProRatio != 0.8 {
		t.Fatalf("totals = %+v", report.Totals)
	}
	if report.Discrepancy.NewAPISuccessRequests != 12 || report.Discrepancy.CPASuccessRequests != 6 {
		t.Fatalf("discrepancy = %+v", report.Discrepancy)
	}
}

func TestReportUsesQueryCostOverride(t *testing.T) {
	t.Parallel()
	cfg := Config{Host: "db.internal", Database: "newapi", ProMonthlyCost: 31}.normalized()
	svc := NewService(cfg, nil)
	svc.logs = fakeLogs{rows: []LogRow{
		{Day: "2026-10-27", Model: "gemini-3-flash", Requests: 2, Quota: 500000},
	}}
	svc.tiers = fakeTiers{rows: []TierRow{
		{Day: "2026-10-27", Tier: TierUltra, Requests: 1, TotalTokens: 10, ActiveCredentials: 4},
	}}
	override := CostSettings{UltraDailyCost: 3}
	report, errReport := svc.Report(context.Background(), Query{Cost: &override, FromDate: "2026-10-27", ToDate: "2026-10-27"})
	if errReport != nil {
		t.Fatal(errReport)
	}
	if report.CostSource != "panel" || svc.FileCost().ProMonthlyCost != 31 {
		t.Fatalf("source %s file %+v", report.CostSource, svc.FileCost())
	}
	ultra := report.Tiers[1]
	if ultra.Tier != TierUltra || ultra.CostUSD == nil || *ultra.CostUSD != 12 || ultra.USD != 1 {
		t.Fatalf("ultra = %+v", ultra)
	}
	if ultra.GrossProfitUSD == nil || *ultra.GrossProfitUSD != -11 {
		t.Fatalf("profit = %v", ultra.GrossProfitUSD)
	}
}

func TestReportReturnsSourceError(t *testing.T) {
	t.Parallel()
	cfg := Config{Host: "db.internal", Database: "newapi"}.normalized()
	svc := NewService(cfg, nil)
	svc.logs = fakeLogs{err: ErrSourceUnavailable}
	_, errReport := svc.Report(context.Background(), Query{})
	if !errors.Is(errReport, ErrSourceUnavailable) {
		t.Fatalf("error = %v", errReport)
	}
}
