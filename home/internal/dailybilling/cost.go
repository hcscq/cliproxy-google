package dailybilling

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

// CostSettings is the per-credential cost basis. Zero means unset.
// A daily cost greater than zero replaces that tier's monthly amortization.
// MonthDays 0 uses the calendar length of each reported day.
type CostSettings struct {
	ProMonthlyCost   float64 `json:"pro_monthly_cost"`
	UltraMonthlyCost float64 `json:"ultra_monthly_cost"`
	OtherMonthlyCost float64 `json:"other_monthly_cost"`
	ProDailyCost     float64 `json:"pro_daily_cost"`
	UltraDailyCost   float64 `json:"ultra_daily_cost"`
	OtherDailyCost   float64 `json:"other_daily_cost"`
	MonthDays        int     `json:"month_days"`
}

// Configured reports whether any tier has a positive monthly or daily cost.
func (s CostSettings) Configured() bool {
	return s.ProMonthlyCost > 0 || s.UltraMonthlyCost > 0 || s.OtherMonthlyCost > 0 ||
		s.ProDailyCost > 0 || s.UltraDailyCost > 0 || s.OtherDailyCost > 0
}

// Validate rejects negative, non-finite, or out-of-range cost inputs.
func (s CostSettings) Validate() error {
	if !validMoney(s.ProMonthlyCost) || !validMoney(s.UltraMonthlyCost) || !validMoney(s.OtherMonthlyCost) ||
		!validMoney(s.ProDailyCost) || !validMoney(s.UltraDailyCost) || !validMoney(s.OtherDailyCost) {
		return ErrInvalidCost
	}
	if s.MonthDays < 0 || s.MonthDays > 31 {
		return ErrInvalidCost
	}
	return nil
}

// EncodeCostSettings validates and JSON-encodes a panel override.
func EncodeCostSettings(settings CostSettings) ([]byte, error) {
	if errValidate := settings.Validate(); errValidate != nil {
		return nil, errValidate
	}
	return json.Marshal(settings)
}

// DecodeCostSettings parses a stored panel override.
func DecodeCostSettings(raw []byte) (CostSettings, error) {
	var settings CostSettings
	if errDecode := json.Unmarshal(raw, &settings); errDecode != nil {
		return CostSettings{}, ErrInvalidCost
	}
	if errValidate := settings.Validate(); errValidate != nil {
		return CostSettings{}, errValidate
	}
	return settings, nil
}

// CostSettings returns the cost basis currently on the config.
func (c Config) CostSettings() CostSettings {
	return CostSettings{
		ProMonthlyCost:   c.ProMonthlyCost,
		UltraMonthlyCost: c.UltraMonthlyCost,
		OtherMonthlyCost: c.OtherMonthlyCost,
		ProDailyCost:     c.ProDailyCost,
		UltraDailyCost:   c.UltraDailyCost,
		OtherDailyCost:   c.OtherDailyCost,
		MonthDays:        c.CostMonthDays,
	}
}

// WithCost replaces the cost basis. Invalid numbers are treated as unset.
func (c Config) WithCost(settings CostSettings) Config {
	c.ProMonthlyCost = clampMoney(settings.ProMonthlyCost)
	c.UltraMonthlyCost = clampMoney(settings.UltraMonthlyCost)
	c.OtherMonthlyCost = clampMoney(settings.OtherMonthlyCost)
	c.ProDailyCost = clampMoney(settings.ProDailyCost)
	c.UltraDailyCost = clampMoney(settings.UltraDailyCost)
	c.OtherDailyCost = clampMoney(settings.OtherDailyCost)
	if settings.MonthDays < 0 || settings.MonthDays > 31 {
		c.CostMonthDays = 0
	} else {
		c.CostMonthDays = settings.MonthDays
	}
	return c
}

// CostConfigured reports whether the report should show cost and margin.
func (c Config) CostConfigured() bool {
	return c.CostSettings().Configured()
}

func (c Config) dailyUnit(tier, day string) float64 {
	monthly, daily := c.tierCost(tier)
	if daily > 0 {
		return daily
	}
	if monthly <= 0 {
		return 0
	}
	days := c.CostMonthDays
	if days <= 0 {
		days = daysInMonth(day)
	}
	if days <= 0 {
		return 0
	}
	return monthly / float64(days)
}

func (c Config) tierCost(tier string) (monthly, daily float64) {
	switch normalizeTier(tier) {
	case TierPro:
		return c.ProMonthlyCost, c.ProDailyCost
	case TierUltra:
		return c.UltraMonthlyCost, c.UltraDailyCost
	default:
		return c.OtherMonthlyCost, c.OtherDailyCost
	}
}

func daysInMonth(day string) int {
	parsed, errParse := time.Parse("2006-01-02", strings.TrimSpace(day))
	if errParse != nil {
		return 0
	}
	return time.Date(parsed.Year(), parsed.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func validMoney(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func clampMoney(value float64) float64 {
	if !validMoney(value) {
		return 0
	}
	return value
}

func roundMoney(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

func moneyPtr(value float64) *float64 {
	return &value
}

func summarizeDay(rows []Row, active map[string]int64, cfg Config, day string) []TierSummary {
	out := make([]TierSummary, len(tierOrder))
	index := map[string]int{}
	for i, tier := range tierOrder {
		index[tier] = i
		out[i] = TierSummary{Tier: tier, Models: []ModelLine{}}
		if active != nil {
			out[i].ActiveCredentials = active[tier]
		}
	}
	for _, row := range rows {
		slot := index[normalizeTier(row.Tier)]
		out[slot].Requests += row.Requests
		out[slot].PromptTokens += row.PromptTokens
		out[slot].CompletionTokens += row.CompletionTokens
		out[slot].Quota += row.Quota
		out[slot].Models = append(out[slot].Models, ModelLine{
			Model:            row.Model,
			Requests:         row.Requests,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			Quota:            row.Quota,
			USD:              row.USD,
		})
	}
	for i := range out {
		out[i].USD = quotaUSD(out[i].Quota, cfg.QuotaPerUnit)
		applyTierCost(&out[i], cfg, day)
	}
	return out
}

func applyTierCost(summary *TierSummary, cfg Config, day string) {
	if summary == nil || !cfg.CostConfigured() {
		return
	}
	unit := roundMoney(cfg.dailyUnit(summary.Tier, day))
	cost := roundMoney(float64(summary.ActiveCredentials) * unit)
	profit := roundMoney(summary.USD - cost)
	summary.DailyUnitUSD = moneyPtr(unit)
	summary.CostUSD = moneyPtr(cost)
	summary.GrossProfitUSD = moneyPtr(profit)
	if summary.USD > 0 {
		margin := roundMoney(profit / summary.USD)
		summary.GrossMargin = &margin
	}
}

func mergeDays(days []Day, cfg Config) []TierSummary {
	out := make([]TierSummary, len(tierOrder))
	index := map[string]int{}
	models := make([]map[string]*ModelLine, len(tierOrder))
	for i, tier := range tierOrder {
		index[tier] = i
		out[i] = TierSummary{Tier: tier, Models: []ModelLine{}}
		models[i] = map[string]*ModelLine{}
	}
	costSum := make([]float64, len(tierOrder))
	for _, day := range days {
		for _, tier := range day.Tiers {
			slot, ok := index[tier.Tier]
			if !ok {
				continue
			}
			out[slot].Requests += tier.Requests
			out[slot].PromptTokens += tier.PromptTokens
			out[slot].CompletionTokens += tier.CompletionTokens
			out[slot].Quota += tier.Quota
			out[slot].ActiveCredentials += tier.ActiveCredentials
			if tier.CostUSD != nil {
				costSum[slot] += *tier.CostUSD
			}
			for _, model := range tier.Models {
				line := models[slot][model.Model]
				if line == nil {
					copied := model
					models[slot][model.Model] = &copied
					continue
				}
				line.Requests += model.Requests
				line.PromptTokens += model.PromptTokens
				line.CompletionTokens += model.CompletionTokens
				line.Quota += model.Quota
			}
		}
	}
	for i := range out {
		out[i].USD = quotaUSD(out[i].Quota, cfg.QuotaPerUnit)
		out[i].ActiveCredentialDays = out[i].ActiveCredentials
		names := make([]string, 0, len(models[i]))
		for name := range models[i] {
			names = append(names, name)
		}
		sort.Strings(names)
		lines := make([]ModelLine, 0, len(names))
		for _, name := range names {
			line := models[i][name]
			line.USD = quotaUSD(line.Quota, cfg.QuotaPerUnit)
			lines = append(lines, *line)
		}
		out[i].Models = lines
		if cfg.CostConfigured() {
			cost := roundMoney(costSum[i])
			profit := roundMoney(out[i].USD - cost)
			out[i].CostUSD = moneyPtr(cost)
			out[i].GrossProfitUSD = moneyPtr(profit)
			if out[i].USD > 0 {
				margin := roundMoney(profit / out[i].USD)
				out[i].GrossMargin = &margin
			}
		}
	}
	return out
}
