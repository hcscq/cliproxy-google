package dailybilling

import (
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
)

const (
	TierPro   = "pro"
	TierUltra = "ultra"
	TierOther = "other"

	// UnconfiguredMessage is the panel copy when the new-api database is absent.
	UnconfiguredMessage = "未配置 new-api 数据源"
	// EstimateNote describes the tier split. It is not a per-request attribution.
	EstimateNote = "pro/ultra/other 按当日 CPA 成功请求的 total_tokens 占比分摊 new-api 计费（全模型，不是逐请求归因）。"
	// CostNote describes the credential-cost estimate. It is not an invoice.
	CostNote = "当日成本 ≈ 当天该档有成功请求的去重凭据数 × 日均摊。日均摊 = 每凭据月成本 ÷ 当月天数；设置了每凭据日成本时改用日成本。毛利 = 估算收入 − 估算成本。"

	DefaultQuotaPerUnit = 500000
	DefaultGroupRatio   = 0.3
	// DefaultChannelID is 0: every channel. Set channel-id or channel-ids to narrow.
	DefaultChannelID = 0
	// DefaultModelPrefix is empty: every model. Set model-prefix or models to narrow.
	DefaultModelPrefix = ""
	// DefaultModelExclude is empty. Set model-exclude (for example -disabled) to drop names.
	DefaultModelExclude = ""
	DefaultProTier      = "g1-pro-tier"
	DefaultUltraTier    = "g1-ultra-tier"
	DefaultSuccessType  = 2
	DefaultTimezone     = "UTC"
	DefaultCreatedAt    = "unix"
	MaxRangeDays        = 93
)

var tierOrder = []string{TierPro, TierUltra, TierOther}

// LogRow is one new-api aggregate: a calendar day and a model name.
type LogRow struct {
	Day              string
	Model            string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	Quota            int64
}

// TierRow is one CPA aggregate: successful tokens and distinct credentials for a day and tier.
type TierRow struct {
	Day               string
	Tier              string
	Requests          int64
	TotalTokens       int64
	ActiveCredentials int64
}

// Query is an inclusive calendar range. To is the exclusive instant after ToDate.
type Query struct {
	From     time.Time
	To       time.Time
	Timezone string
	FromDate string
	ToDate   string
	// Cost, when set, replaces the file and environment cost basis for this report.
	Cost *CostSettings
}

// Report is the daily billing payload.
type Report struct {
	Configured     bool          `json:"configured"`
	Message        string        `json:"message,omitempty"`
	Estimated      bool          `json:"estimated"`
	EstimateNote   string        `json:"estimate_note,omitempty"`
	From           string        `json:"from,omitempty"`
	To             string        `json:"to,omitempty"`
	Timezone       string        `json:"timezone,omitempty"`
	QuotaPerUnit   float64       `json:"quota_per_unit,omitempty"`
	GroupRatio     float64       `json:"group_ratio,omitempty"`
	ChannelID      int           `json:"channel_id,omitempty"`
	ChannelIDs     []int         `json:"channel_ids,omitempty"`
	ModelPrefix    string        `json:"model_prefix,omitempty"`
	Models         []string      `json:"models,omitempty"`
	ModelExclude   string        `json:"model_exclude,omitempty"`
	CostConfigured bool          `json:"cost_configured"`
	CostNote       string        `json:"cost_note,omitempty"`
	CostSource     string        `json:"cost_source,omitempty"`
	CostSettings   CostSettings  `json:"cost_settings"`
	Discrepancy    *Discrepancy  `json:"discrepancy,omitempty"`
	Totals         Totals        `json:"totals"`
	Tiers          []TierSummary `json:"tiers"`
	Days           []Day         `json:"days"`
}

// TierSummary is one subscription tier for a day or for the whole range.
// On a day, ActiveCredentials is the distinct CPA credentials with a successful
// request. On the range, ActiveCredentials and ActiveCredentialDays are the sum
// of those daily counts (credential-days), not a distinct count across days.
// Cost fields are set only when a cost basis is configured.
type TierSummary struct {
	Tier                 string      `json:"tier"`
	Requests             int64       `json:"requests"`
	PromptTokens         int64       `json:"prompt_tokens"`
	CompletionTokens     int64       `json:"completion_tokens"`
	Quota                int64       `json:"quota"`
	USD                  float64     `json:"usd"`
	ActiveCredentials    int64       `json:"active_credentials"`
	ActiveCredentialDays int64       `json:"active_credential_days,omitempty"`
	DailyUnitUSD         *float64    `json:"daily_unit_usd,omitempty"`
	CostUSD              *float64    `json:"cost_usd,omitempty"`
	GrossProfitUSD       *float64    `json:"gross_profit_usd,omitempty"`
	GrossMargin          *float64    `json:"gross_margin,omitempty"`
	Models               []ModelLine `json:"models"`
}

// ModelLine is one model_name inside a tier. gemini-4-argon variants stay separate.
type ModelLine struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Quota            int64   `json:"quota"`
	USD              float64 `json:"usd"`
}

// Discrepancy compares successful request counts. new-api minus CPA.
type Discrepancy struct {
	NewAPISuccessRequests int64 `json:"new_api_success_requests"`
	CPASuccessRequests    int64 `json:"cpa_success_requests"`
	Difference            int64 `json:"difference"`
}

// Totals are summed after the integer allocation so quota still matches new-api.
type Totals struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Quota            int64   `json:"quota"`
	USD              float64 `json:"usd"`
	ProQuota         int64   `json:"pro_quota"`
	UltraQuota       int64   `json:"ultra_quota"`
	OtherQuota       int64   `json:"other_quota"`
	ProUSD           float64 `json:"pro_usd"`
	UltraUSD         float64 `json:"ultra_usd"`
	OtherUSD         float64 `json:"other_usd"`
	ProRatio         float64 `json:"pro_ratio"`
}

// Day is one calendar day in the reporting timezone.
type Day struct {
	Date                  string             `json:"date"`
	TierShares            map[string]float64 `json:"tier_shares"`
	Unallocated           bool               `json:"unallocated"`
	NewAPISuccessRequests int64              `json:"new_api_success_requests"`
	CPASuccessRequests    int64              `json:"cpa_success_requests"`
	Rows                  []Row              `json:"rows"`
	Tiers                 []TierSummary      `json:"tiers"`
	Totals                Totals             `json:"totals"`
}

// Row is the estimated slice of one variant for one tier.
type Row struct {
	Tier             string  `json:"tier"`
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Quota            int64   `json:"quota"`
	USD              float64 `json:"usd"`
}

// ResolveRange parses YYYY-MM-DD bounds. Empty bounds default to yesterday and today.
func ResolveRange(now time.Time, fromStr, toStr, tz string) (Query, error) {
	if strings.TrimSpace(tz) == "" {
		tz = DefaultTimezone
	}
	loc, errLoc := time.LoadLocation(strings.TrimSpace(tz))
	if errLoc != nil {
		return Query{}, ErrInvalidTimezone
	}
	if now.IsZero() {
		now = time.Now()
	}
	fromStr = strings.TrimSpace(fromStr)
	toStr = strings.TrimSpace(toStr)
	today := dateIn(now, loc)

	var fromDate, toDate time.Time
	var errParse error
	switch {
	case fromStr == "" && toStr == "":
		toDate = today
		fromDate = today.AddDate(0, 0, -1)
	case fromStr == "":
		toDate, errParse = parseDate(toStr, loc)
		if errParse != nil {
			return Query{}, ErrInvalidRange
		}
		fromDate = toDate.AddDate(0, 0, -1)
	case toStr == "":
		fromDate, errParse = parseDate(fromStr, loc)
		if errParse != nil {
			return Query{}, ErrInvalidRange
		}
		toDate = today
		if toDate.Before(fromDate) {
			toDate = fromDate
		}
	default:
		fromDate, errParse = parseDate(fromStr, loc)
		if errParse != nil {
			return Query{}, ErrInvalidRange
		}
		toDate, errParse = parseDate(toStr, loc)
		if errParse != nil {
			return Query{}, ErrInvalidRange
		}
	}
	if toDate.Before(fromDate) {
		return Query{}, ErrInvalidRange
	}
	toExclusive := toDate.AddDate(0, 0, 1)
	days := 0
	for cursor := fromDate; cursor.Before(toExclusive); cursor = cursor.AddDate(0, 0, 1) {
		days++
		if days > MaxRangeDays {
			return Query{}, ErrInvalidRange
		}
	}
	return Query{
		From:     fromDate,
		To:       toExclusive,
		Timezone: loc.String(),
		FromDate: fromDate.Format("2006-01-02"),
		ToDate:   toDate.Format("2006-01-02"),
	}, nil
}

func dateIn(now time.Time, loc *time.Location) time.Time {
	year, month, day := now.In(loc).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, loc)
}

func parseDate(value string, loc *time.Location) (time.Time, error) {
	parsed, errParse := time.ParseInLocation("2006-01-02", value, loc)
	if errParse != nil {
		return time.Time{}, errParse
	}
	return parsed, nil
}

// Build allocates new-api totals onto pro/ultra/other using that day's CPA token weights.
// USD is quota / QuotaPerUnit. group_ratio is not applied again; new-api quota already includes it.
func Build(cfg Config, query Query, logs []LogRow, tiers []TierRow) Report {
	cfg = cfg.normalized()
	weights := map[string]map[string]int64{}
	tierRequests := map[string]map[string]int64{}
	active := map[string]map[string]int64{}
	var cpaTotal int64
	for _, row := range tiers {
		day := strings.TrimSpace(row.Day)
		if day == "" {
			continue
		}
		tier := normalizeTier(row.Tier)
		if weights[day] == nil {
			weights[day] = map[string]int64{}
			tierRequests[day] = map[string]int64{}
			active[day] = map[string]int64{}
		}
		if row.TotalTokens > 0 {
			weights[day][tier] += row.TotalTokens
		}
		if row.Requests > 0 {
			tierRequests[day][tier] += row.Requests
			cpaTotal += row.Requests
		}
		if row.ActiveCredentials > 0 {
			active[day][tier] += row.ActiveCredentials
		}
	}

	byDay := map[string][]LogRow{}
	var dayOrder []string
	var newTotal int64
	for _, row := range logs {
		day := strings.TrimSpace(row.Day)
		if day == "" || strings.TrimSpace(row.Model) == "" {
			continue
		}
		if _, seen := byDay[day]; !seen {
			dayOrder = append(dayOrder, day)
		}
		byDay[day] = append(byDay[day], row)
		if row.Requests > 0 {
			newTotal += row.Requests
		}
	}
	for day := range weights {
		if _, seen := byDay[day]; seen {
			continue
		}
		dayOrder = append(dayOrder, day)
	}
	sort.Strings(dayOrder)

	report := Report{
		Configured:   true,
		Estimated:    true,
		EstimateNote: EstimateNote,
		From:         query.FromDate,
		To:           query.ToDate,
		Timezone:     query.Timezone,
		QuotaPerUnit: cfg.QuotaPerUnit,
		GroupRatio:   cfg.GroupRatio,
		ChannelID:    cfg.ChannelID,
		Days:         []Day{},
		Discrepancy: &Discrepancy{
			NewAPISuccessRequests: newTotal,
			CPASuccessRequests:    cpaTotal,
			Difference:            newTotal - cpaTotal,
		},
	}

	for _, day := range dayOrder {
		weight := weights[day]
		unallocated := tokenSum(weight) == 0
		dayReport := Day{
			Date:        day,
			TierShares:  roundShares(sharesFromWeights(weight)),
			Unallocated: unallocated,
			Rows:        []Row{},
		}
		for _, count := range tierRequests[day] {
			dayReport.CPASuccessRequests += count
		}
		sources := append([]LogRow(nil), byDay[day]...)
		sort.Slice(sources, func(i, j int) bool { return sources[i].Model < sources[j].Model })
		for _, src := range sources {
			dayReport.NewAPISuccessRequests += src.Requests
			requests := allocate(src.Requests, weight)
			prompt := allocate(src.PromptTokens, weight)
			completion := allocate(src.CompletionTokens, weight)
			quota := allocate(src.Quota, weight)
			for _, tier := range tierOrder {
				if requests[tier] == 0 && prompt[tier] == 0 && completion[tier] == 0 && quota[tier] == 0 {
					continue
				}
				dayReport.Rows = append(dayReport.Rows, Row{
					Tier:             tier,
					Model:            src.Model,
					Requests:         requests[tier],
					PromptTokens:     prompt[tier],
					CompletionTokens: completion[tier],
					Quota:            quota[tier],
					USD:              quotaUSD(quota[tier], cfg.QuotaPerUnit),
				})
			}
		}
		dayReport.Totals = totalsFromRows(dayReport.Rows, cfg.QuotaPerUnit)
		dayReport.Tiers = summarizeDay(dayReport.Rows, active[day], cfg, day)
		report.Days = append(report.Days, dayReport)
	}
	report.Totals = totalsFromDays(report.Days, cfg.QuotaPerUnit)
	report.Tiers = mergeDays(report.Days, cfg)
	report.CostSettings = cfg.CostSettings()
	report.CostConfigured = cfg.CostConfigured()
	if report.CostConfigured {
		report.CostNote = CostNote
	}
	report.ModelPrefix = cfg.ModelPrefix
	report.Models = cfg.Models
	report.ModelExclude = cfg.ModelExclude
	applyChannelFields(&report, cfg)
	return report
}

func applyChannelFields(report *Report, cfg Config) {
	ids := cfg.channelIDs()
	switch len(ids) {
	case 0:
		report.ChannelID = 0
	case 1:
		report.ChannelID = ids[0]
	default:
		report.ChannelID = 0
		report.ChannelIDs = ids
	}
}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TierPro, TierUltra, TierOther:
		return strings.ToLower(strings.TrimSpace(tier))
	default:
		return TierOther
	}
}

func tokenSum(weights map[string]int64) int64 {
	var sum int64
	for _, tier := range tierOrder {
		sum += weights[tier]
	}
	return sum
}

func sharesFromWeights(weights map[string]int64) map[string]float64 {
	out := map[string]float64{TierPro: 0, TierUltra: 0, TierOther: 0}
	sum := tokenSum(weights)
	if sum <= 0 {
		out[TierOther] = 1
		return out
	}
	for _, tier := range tierOrder {
		out[tier] = float64(weights[tier]) / float64(sum)
	}
	return out
}

func roundShares(shares map[string]float64) map[string]float64 {
	out := map[string]float64{}
	var sum float64
	largest := TierOther
	for _, tier := range tierOrder {
		value := math.Round(shares[tier]*1e6) / 1e6
		out[tier] = value
		sum += value
		if value >= out[largest] {
			largest = tier
		}
	}
	drift := math.Round((1-sum)*1e6) / 1e6
	if drift != 0 {
		out[largest] = math.Round((out[largest]+drift)*1e6) / 1e6
	}
	return out
}

// allocate splits total by integer weights using the largest-remainder method.
// A zero weight sum assigns everything to other (no CPA sample that day).
func allocate(total int64, weights map[string]int64) map[string]int64 {
	out := map[string]int64{TierPro: 0, TierUltra: 0, TierOther: 0}
	if total <= 0 {
		return out
	}
	sum := tokenSum(weights)
	if sum <= 0 {
		out[TierOther] = total
		return out
	}
	type remainder struct {
		tier string
		rem  int64
		idx  int
	}
	parts := make([]remainder, 0, len(tierOrder))
	sumBig := big.NewInt(sum)
	var used int64
	for idx, tier := range tierOrder {
		weight := weights[tier]
		if weight < 0 {
			weight = 0
		}
		product := new(big.Int).Mul(big.NewInt(total), big.NewInt(weight))
		quot := new(big.Int)
		rem := new(big.Int)
		quot.QuoRem(product, sumBig, rem)
		if !quot.IsInt64() {
			out[TierOther] += total - used
			return out
		}
		out[tier] = quot.Int64()
		used += out[tier]
		parts = append(parts, remainder{tier: tier, rem: rem.Int64(), idx: idx})
	}
	left := total - used
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].rem != parts[j].rem {
			return parts[i].rem > parts[j].rem
		}
		return parts[i].idx < parts[j].idx
	})
	for left > 0 {
		progressed := false
		for _, part := range parts {
			if left == 0 {
				break
			}
			if weights[part.tier] <= 0 {
				continue
			}
			out[part.tier]++
			left--
			progressed = true
		}
		if !progressed {
			out[TierOther] += left
			break
		}
	}
	return out
}

func quotaUSD(quota int64, perUnit float64) float64 {
	if perUnit <= 0 {
		perUnit = DefaultQuotaPerUnit
	}
	return math.Round((float64(quota)/perUnit)*1e6) / 1e6
}

func totalsFromRows(rows []Row, perUnit float64) Totals {
	var totals Totals
	for _, row := range rows {
		totals.Requests += row.Requests
		totals.PromptTokens += row.PromptTokens
		totals.CompletionTokens += row.CompletionTokens
		totals.Quota += row.Quota
		switch row.Tier {
		case TierPro:
			totals.ProQuota += row.Quota
		case TierUltra:
			totals.UltraQuota += row.Quota
		default:
			totals.OtherQuota += row.Quota
		}
	}
	return finishTotals(totals, perUnit)
}

func totalsFromDays(days []Day, perUnit float64) Totals {
	var totals Totals
	for _, day := range days {
		totals.Requests += day.Totals.Requests
		totals.PromptTokens += day.Totals.PromptTokens
		totals.CompletionTokens += day.Totals.CompletionTokens
		totals.Quota += day.Totals.Quota
		totals.ProQuota += day.Totals.ProQuota
		totals.UltraQuota += day.Totals.UltraQuota
		totals.OtherQuota += day.Totals.OtherQuota
	}
	return finishTotals(totals, perUnit)
}

func finishTotals(totals Totals, perUnit float64) Totals {
	totals.USD = quotaUSD(totals.Quota, perUnit)
	totals.ProUSD = quotaUSD(totals.ProQuota, perUnit)
	totals.UltraUSD = quotaUSD(totals.UltraQuota, perUnit)
	totals.OtherUSD = quotaUSD(totals.OtherQuota, perUnit)
	if totals.Quota > 0 {
		totals.ProRatio = math.Round((float64(totals.ProQuota)/float64(totals.Quota))*1e6) / 1e6
	}
	return totals
}
