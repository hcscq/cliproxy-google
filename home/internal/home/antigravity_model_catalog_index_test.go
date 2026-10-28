package home

import (
	"testing"
	"time"
)

func floatPtr(v float64) *float64 { return &v }

func TestModelCatalogIndexIncremental(t *testing.T) {
	idx := newModelCatalogIndex()
	reset := time.Date(2026, 10, 17, 5, 36, 57, 0, time.UTC)
	idx.replace("a", &antigravityCatalogHealth{models: map[string]CatalogModelHealth{
		"gemini-4-argon-medium":   {ID: "gemini-4-argon-medium", QuotaRemaining: floatPtr(0.4), QuotaResetTime: reset.Format(time.RFC3339)},
		"gemini-4-argon-disabled": {ID: "gemini-4-argon-disabled", QuotaRemaining: floatPtr(1)},
	}})
	idx.replace("b", &antigravityCatalogHealth{models: map[string]CatalogModelHealth{
		"gemini-4-argon-medium": {ID: "Gemini-4-Argon-Medium", QuotaRemaining: floatPtr(0)},
	}})

	counts := idx.counts()
	if counts["gemini-4-argon-medium"] != 2 {
		t.Fatalf("counts = %#v, want medium=2 (disabled placeholder excluded)", counts)
	}
	if _, ok := counts["gemini-4-argon-disabled"]; ok {
		t.Fatalf("disabled placeholder was indexed: %#v", counts)
	}
	quotas := idx.quotas("GEMINI-4-ARGON-MEDIUM")
	if len(quotas) != 2 {
		t.Fatalf("quotas = %#v, want both credentials, looked up case-insensitively", quotas)
	}
	if quotas["a"].Remaining == nil || *quotas["a"].Remaining != 0.4 || !quotas["a"].Reset.Equal(reset) {
		t.Fatalf("quota a = %#v", quotas["a"])
	}
	if quotas["b"].Remaining == nil || *quotas["b"].Remaining != 0 {
		t.Fatalf("quota b = %#v", quotas["b"])
	}

	// Updating one credential must not rescan the other: count stays 2, quota changes.
	idx.replace("a", &antigravityCatalogHealth{models: map[string]CatalogModelHealth{
		"gemini-4-argon-medium": {ID: "gemini-4-argon-medium", QuotaRemaining: floatPtr(0.1)},
	}})
	if idx.counts()["gemini-4-argon-medium"] != 2 {
		t.Fatalf("count after update = %#v", idx.counts())
	}
	if got := idx.quotas("gemini-4-argon-medium")["a"].Remaining; got == nil || *got != 0.1 {
		t.Fatalf("updated remaining = %v", got)
	}

	idx.forget("b")
	if idx.counts()["gemini-4-argon-medium"] != 1 {
		t.Fatalf("count after forget = %#v", idx.counts())
	}
	if _, ok := idx.quotas("gemini-4-argon-medium")["b"]; ok {
		t.Fatal("forgotten credential still indexed")
	}
	idx.forget("a")
	if len(idx.counts()) != 0 || idx.quotas("gemini-4-argon-medium") != nil {
		t.Fatalf("index not empty after last forget: counts=%#v", idx.counts())
	}
}

func TestModelCatalogIndexNoQuotaInfo(t *testing.T) {
	idx := newModelCatalogIndex()
	idx.replace("a", &antigravityCatalogHealth{models: map[string]CatalogModelHealth{
		"gemini-3-flash": {ID: "gemini-3-flash"},
	}})
	q := idx.quotas("gemini-3-flash")["a"]
	if q.Remaining != nil || !q.Reset.IsZero() {
		t.Fatalf("missing quotaInfo = %#v, want nil remaining", q)
	}
}
