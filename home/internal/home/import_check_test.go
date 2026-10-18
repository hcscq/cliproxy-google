package home

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

func TestClassifyHealth(t *testing.T) {
	cfg := (&config.Config{}).ImportCheckSettings()
	fresh := HealthInputs{QuotaWindows: 4, QuotaMin: 1, CatalogOK: true, CatalogCount: 22}
	okProbe := HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 200}
	cases := []struct {
		name string
		p    HealthProbeResult
		in   HealthInputs
		want []string
	}{
		{"pass", okProbe, fresh, nil},
		{"not pro", HealthProbeResult{TierStatus: 200, Tier: "free-tier", GenStatus: 200}, fresh, []string{HealthTier}},
		{"validation via loadCodeAssist", HealthProbeResult{TierStatus: 200, IneligibleCode: "VALIDATION_REQUIRED", IneligibleText: "verify at https://x", GenStatus: 200}, fresh, []string{HealthNeedsVerify}},
		{"verify on generate 403", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 403, GenBody: `{"error":{"code":403,"message":"Please verify your account","details":[{"reason":"VALIDATION_REQUIRED"}]}}`}, fresh, []string{HealthNeedsVerify}},
		{"tos 403", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 403, GenBody: "This service has been disabled in this account for violation of Terms of Service."}, fresh, []string{HealthTOS}},
		{"permission 403", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 403, GenBody: `{"error":{"status":"PERMISSION_DENIED"}}`}, fresh, []string{HealthPermission}},
		{"429 rate", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 429, GenBody: "RESOURCE_EXHAUSTED"}, fresh, []string{HealthRateLimited}},
		{"429 quota exhausted", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 429, GenBody: `"reason": "QUOTA_EXHAUSTED"`}, fresh, []string{HealthQuotaUsed}},
		{"quota used", okProbe, HealthInputs{QuotaWindows: 4, QuotaMin: 0.6, CatalogOK: true, CatalogCount: 22}, []string{HealthQuotaUsed}},
		{"no quota no catalog", okProbe, HealthInputs{}, []string{HealthQuotaNone, HealthNoCatalog}},
		{"no egress", HealthProbeResult{NoEgress: true}, HealthInputs{}, []string{HealthNoEgress}},
		{"onboarding 400", HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", GenStatus: 400, GenBody: "FAILED_PRECONDITION: user must onboard"}, fresh, []string{HealthNeedsVerify}},
		{"no tier", HealthProbeResult{TierStatus: 200, GenStatus: 200}, fresh, []string{HealthNeedsVerify}},
	}
	for _, tc := range cases {
		v := ClassifyHealth(tc.p, tc.in, cfg)
		if len(tc.want) == 0 {
			if v.Status != "pass" || len(v.Categories) != 0 {
				t.Errorf("%s: got %+v, want pass", tc.name, v)
			}
			continue
		}
		if v.Status != "fail" || len(v.Categories) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, v.Categories, tc.want)
			continue
		}
		for i, c := range tc.want {
			if v.Categories[i] != c {
				t.Errorf("%s: got %v, want %v", tc.name, v.Categories, tc.want)
			}
		}
		if (tc.p.GenBody != "" || tc.p.IneligibleText != "") && v.Evidence == "" {
			t.Errorf("%s: raw upstream text must be kept as evidence", tc.name)
		}
	}
	// Expected tier "any" accepts free.
	any := (&config.Config{ImportCheckExpectedTier: "any"}).ImportCheckSettings()
	if v := ClassifyHealth(HealthProbeResult{TierStatus: 200, Tier: "free-tier", GenStatus: 200}, fresh, any); v.Status != "pass" {
		t.Fatalf("any tier: %+v", v)
	}
}

func TestImportCheckSettingsDefaults(t *testing.T) {
	s := (&config.Config{}).ImportCheckSettings()
	if !s.OnImport || s.ExpectedTier != "pro" || s.QuotaMin != 0.95 || s.Concurrency != 3 || s.Promote != "pass" || s.BlockFailed || s.Model == "" {
		t.Fatalf("defaults = %+v", s)
	}
	off := false
	s = (&config.Config{ImportCheckOnImport: &off, ImportCheckConcurrency: 99, ImportCheckPromote: "bogus", ImportCheckQuotaMinPct: 80}).ImportCheckSettings()
	if s.OnImport || s.Concurrency != 16 || s.Promote != "pass" || s.QuotaMin != 0.8 {
		t.Fatalf("parsed = %+v", s)
	}
}

func TestClassifyRegionRestriction(t *testing.T) {
	fresh := HealthInputs{QuotaWindows: 4, QuotaMin: 1, CatalogOK: true, CatalogCount: 22}
	codes, text := parseIneligibleTiers([]byte(`{"paidTier":{"id":"g1-pro-tier"},"ineligibleTiers":[{"reasonCode":"INELIGIBLE_ACCOUNT","reasonMessage":"x"},{"reasonCode":"UNSUPPORTED_LOCATION","reasonMessage":"Your current account is not eligible for Antigravity, because it is not currently available in your location."}]}`))
	if codes != "INELIGIBLE_ACCOUNT,UNSUPPORTED_LOCATION" || !strings.Contains(text, "not currently available in your location") {
		t.Fatalf("parse = %q / %q", codes, text)
	}
	p := HealthProbeResult{TierStatus: 200, Tier: "g1-pro-tier", IneligibleCode: codes, IneligibleText: text, GenStatus: 200}
	v := ClassifyHealth(p, fresh, (&config.Config{}).ImportCheckSettings())
	if v.Status != "fail" || len(v.Categories) != 1 || v.Categories[0] != HealthRegion || !strings.Contains(v.Evidence, "location") {
		t.Fatalf("region default on: %+v", v)
	}
	off := false
	if v := ClassifyHealth(p, fresh, (&config.Config{ImportCheckRegionFail: &off}).ImportCheckSettings()); v.Status != "pass" || len(v.Categories) != 1 || v.Categories[0] != HealthRegionWarn {
		t.Fatalf("region off: %+v", v)
	}
}
