package auth

import (
	"testing"
	"time"
)

func TestParseUsageRetryHintsRealAntigravityBodies(t *testing.T) {
	weekly := `{"error":{"code":429,"message":"Individual quota reached. Resets in 126h19m36s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-3.6-flash-medium","quotaResetDelay":"126h19m36.408136128s","quotaResetTimeStamp":"2026-10-17T14:39:04Z"}},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"454776.408136128s"}]}}`
	d, at := parseUsageRetryHints("antigravity", weekly, 429)
	if d == nil || *d < 126*time.Hour || at == nil || !at.Equal(time.Date(2026, 10, 17, 14, 39, 4, 0, time.UTC)) {
		t.Fatalf("weekly hints = %v %v", d, at)
	}
	bare := `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`
	if d, at := parseUsageRetryHints("antigravity", bare, 429); d != nil || at != nil {
		t.Fatalf("bare 429 must carry no hint, got %v %v", d, at)
	}
}
