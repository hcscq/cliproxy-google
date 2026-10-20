package home

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestStallReportFastBlocksPair(t *testing.T) {
	defer coreauth.ClearQuotaBlock("st1", "")
	body := `{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"3.8 慢: no first byte from upstream within 15s","cpa_cooldown_ms":45000}}`
	res := coreauth.NewUsageResult("st1", "antigravity", "gemini-3.8-flash-tiered", 504, body)
	r := &Runtime{}
	r.blockAndPublishStallCooldown("st1", 504, body, res)
	until, ok := coreauth.QuotaBlockedUntil("st1", "gemini-3.8-flash-tiered")
	if !ok || time.Until(until) < 40*time.Second || time.Until(until) > 46*time.Second {
		t.Fatalf("stall block: %v %v", until, ok)
	}
	if _, ok := coreauth.QuotaBlockedUntil("st1", "gemini-2.5-flash"); ok {
		t.Fatal("other model blocked")
	}
	// Non-stall 504 / other errors do not block.
	other := coreauth.NewUsageResult("st2", "antigravity", "gemini-3.8-flash-tiered", 504, `{"error":{"message":"upstream timeout"}}`)
	r.blockAndPublishStallCooldown("st2", 504, `{"error":{"message":"upstream timeout"}}`, other)
	if _, ok := coreauth.QuotaBlockedUntil("st2", "gemini-3.8-flash-tiered"); ok {
		t.Fatal("non-stall blocked")
	}
}
