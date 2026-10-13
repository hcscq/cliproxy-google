package auth

import (
	"testing"
	"time"
)

func TestParseUsageRetryHintsCPACooldown(t *testing.T) {
	d, _ := parseUsageRetryHints("antigravity", `{"error":{"code":504,"message":"慢: x","cpa_cooldown_ms":300000}}`, 504)
	if d == nil || *d != 5*time.Minute {
		t.Fatalf("got %v", d)
	}
	d, _ = parseUsageRetryHints("antigravity", `{"error":{"code":503,"message":"No capacity","cpa_cooldown_ms":10000}}`, 503)
	if d == nil || *d != 10*time.Second {
		t.Fatalf("got %v", d)
	}
	d, _ = parseUsageRetryHints("antigravity", `{"error":{"code":503}}`, 503)
	if d != nil {
		t.Fatalf("got %v", d)
	}
}
