package dailybilling

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestAllocateUsesDailyTokenShares(t *testing.T) {
	t.Parallel()

	cfg := Config{QuotaPerUnit: DefaultQuotaPerUnit, GroupRatio: DefaultGroupRatio, ChannelID: DefaultChannelID}.normalized()
	query := Query{FromDate: "2026-10-27", ToDate: "2026-10-28", Timezone: "UTC"}
	logs := []LogRow{
		{Day: "2026-10-27", Model: "gemini-4-argon-high", Requests: 10, PromptTokens: 1000, CompletionTokens: 400, Quota: 1000},
		{Day: "2026-10-28", Model: "gemini-4-argon", Requests: 5, PromptTokens: 50, CompletionTokens: 20, Quota: 500000},
		{Day: "2026-10-28", Model: "gemini-4-argon-low", Requests: 1, PromptTokens: 10, CompletionTokens: 0, Quota: 10},
	}
	tiers := []TierRow{
		// Day 1 is 70% pro / 30% ultra. Day 2 ultra is almost stopped.
		{Day: "2026-10-27", Tier: TierPro, Requests: 7, TotalTokens: 700},
		{Day: "2026-10-27", Tier: TierUltra, Requests: 3, TotalTokens: 300},
		{Day: "2026-10-28", Tier: TierPro, Requests: 100, TotalTokens: 100},
		{Day: "2026-10-28", Tier: TierUltra, Requests: 1, TotalTokens: 1},
	}

	report := Build(cfg, query, logs, tiers)
	if !report.Estimated || report.Discrepancy == nil {
		t.Fatalf("report = %+v, want an estimate with discrepancy", report)
	}
	if report.Discrepancy.NewAPISuccessRequests != 16 || report.Discrepancy.CPASuccessRequests != 111 {
		t.Fatalf("discrepancy = %+v", report.Discrepancy)
	}
	if report.Discrepancy.Difference != 16-111 {
		t.Fatalf("difference = %d", report.Discrepancy.Difference)
	}
	if len(report.Days) != 2 || report.Days[0].Date != "2026-10-27" || report.Days[1].Date != "2026-10-28" {
		t.Fatalf("days = %+v", report.Days)
	}

	day1 := report.Days[0]
	if math.Abs(day1.TierShares[TierPro]-0.7) > 1e-6 || math.Abs(day1.TierShares[TierUltra]-0.3) > 1e-6 {
		t.Fatalf("day1 shares = %v", day1.TierShares)
	}
	pro, ultra := findTier(t, day1.Rows, "gemini-4-argon-high")
	if pro.Requests != 7 || ultra.Requests != 3 {
		t.Fatalf("day1 requests pro=%d ultra=%d", pro.Requests, ultra.Requests)
	}
	if pro.Quota+ultra.Quota != 1000 || pro.PromptTokens+ultra.PromptTokens != 1000 {
		t.Fatalf("day1 allocation does not sum to new-api totals: %+v %+v", pro, ultra)
	}
	if pro.Quota != 700 || ultra.Quota != 300 {
		t.Fatalf("day1 quota pro=%d ultra=%d, want 700/300", pro.Quota, ultra.Quota)
	}

	day2 := report.Days[1]
	if day2.TierShares[TierUltra] > 0.02 {
		t.Fatalf("day2 ultra share = %v, want the stopped day to stay near zero", day2.TierShares[TierUltra])
	}
	argonPro, _ := findTier(t, day2.Rows, "gemini-4-argon")
	if argonPro.Quota < 490000 {
		t.Fatalf("day2 pro quota = %d, want nearly all of 500000", argonPro.Quota)
	}
	// 500000 quota units are one dollar. group_ratio must not be applied a second time.
	if report.Totals.USD != 1.00202 {
		t.Fatalf("usd = %v, want 1.00202 from quota/500000 (quota 1000+500000+10)", report.Totals.USD)
	}
	if report.Totals.Quota != 501010 {
		t.Fatalf("quota = %d", report.Totals.Quota)
	}
	if report.GroupRatio != DefaultGroupRatio {
		t.Fatalf("group ratio = %v", report.GroupRatio)
	}
}

func TestAllocateRemainderAndUnallocatedDay(t *testing.T) {
	t.Parallel()

	weights := map[string]int64{TierPro: 1, TierUltra: 1, TierOther: 1}
	got := allocate(10, weights)
	if got[TierPro]+got[TierUltra]+got[TierOther] != 10 {
		t.Fatalf("remainder sum = %v", got)
	}
	if got[TierPro] != 4 || got[TierUltra] != 3 || got[TierOther] != 3 {
		t.Fatalf("largest remainder = %v, want pro 4 ultra 3 other 3", got)
	}

	none := allocate(8, nil)
	if none[TierOther] != 8 || none[TierPro] != 0 || none[TierUltra] != 0 {
		t.Fatalf("unallocated = %v", none)
	}

	cfg := Config{QuotaPerUnit: DefaultQuotaPerUnit}.normalized()
	report := Build(cfg, Query{FromDate: "2026-10-28", ToDate: "2026-10-28"}, []LogRow{
		{Day: "2026-10-28", Model: "gemini-4-argon-medium", Requests: 4, PromptTokens: 40, CompletionTokens: 8, Quota: 500000},
	}, nil)
	if len(report.Days) != 1 || !report.Days[0].Unallocated {
		t.Fatalf("day = %+v, want unallocated", report.Days)
	}
	if len(report.Days[0].Rows) != 1 || report.Days[0].Rows[0].Tier != TierOther {
		t.Fatalf("rows = %+v, want a single other row", report.Days[0].Rows)
	}
	if report.Days[0].Rows[0].USD != 1 || report.Totals.ProRatio != 0 {
		t.Fatalf("usd=%v pro_ratio=%v", report.Days[0].Rows[0].USD, report.Totals.ProRatio)
	}
	if report.Days[0].TierShares[TierOther] != 1 {
		t.Fatalf("shares = %v", report.Days[0].TierShares)
	}
}

func TestResolveRangeDefaultsAndLimits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 28, 16, 30, 0, 0, time.UTC)
	utc, errUTC := ResolveRange(now, "", "", "UTC")
	if errUTC != nil {
		t.Fatal(errUTC)
	}
	if utc.FromDate != "2026-10-27" || utc.ToDate != "2026-10-28" {
		t.Fatalf("utc range = %s..%s", utc.FromDate, utc.ToDate)
	}
	shanghai, errSH := ResolveRange(now, "", "", "Asia/Shanghai")
	if errSH != nil {
		t.Fatal(errSH)
	}
	if shanghai.FromDate != "2026-10-28" || shanghai.ToDate != "2026-10-29" {
		t.Fatalf("shanghai range = %s..%s", shanghai.FromDate, shanghai.ToDate)
	}
	if _, errBad := ResolveRange(now, "2026-10-28", "2026-10-01", "UTC"); errBad != ErrInvalidRange {
		t.Fatalf("reversed range error = %v", errBad)
	}
	if _, errTZ := ResolveRange(now, "", "", "Not/AZone"); errTZ != ErrInvalidTimezone {
		t.Fatalf("timezone error = %v", errTZ)
	}
	if _, errLong := ResolveRange(now, "2026-01-01", "2026-05-01", "UTC"); errLong != ErrInvalidRange {
		t.Fatalf("long range error = %v", errLong)
	}
}

func findTier(t *testing.T, rows []Row, model string) (Row, Row) {
	t.Helper()
	var pro, ultra Row
	var sawPro, sawUltra bool
	for _, row := range rows {
		if row.Model != model {
			continue
		}
		switch row.Tier {
		case TierPro:
			pro = row
			sawPro = true
		case TierUltra:
			ultra = row
			sawUltra = true
		}
	}
	if !sawPro || !sawUltra {
		t.Fatalf("model %s rows = %+v, want pro and ultra", model, rows)
	}
	return pro, ultra
}

func TestQuotaUSDDoesNotReapplyGroupRatio(t *testing.T) {
	t.Parallel()
	if quotaUSD(1, DefaultQuotaPerUnit) != 0.000002 {
		t.Fatalf("usd(1) = %v", quotaUSD(1, DefaultQuotaPerUnit))
	}
	if math.Abs(quotaUSD(500000, DefaultQuotaPerUnit)-1) > 0 {
		t.Fatal(quotaUSD(500000, DefaultQuotaPerUnit))
	}
	if strings.Contains(EstimateNote, "逐请求") == false {
		t.Fatal(EstimateNote)
	}
}
