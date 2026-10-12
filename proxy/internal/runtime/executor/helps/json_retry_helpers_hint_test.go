package helps

import (
	"testing"
	"time"
)

func TestParseRetryHints(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetTimeStamp":"` + reset + `"}}]}}`)
	d, err := ParseRetryDelay(body)
	if err != nil || *d < 9*time.Minute || *d > 10*time.Minute {
		t.Fatalf("quotaResetTimeStamp = %v %v", d, err)
	}
	if d, ok := ParseRetryAfterHeader("42"); !ok || *d != 42*time.Second {
		t.Fatalf("Retry-After seconds = %v %v", d, ok)
	}
	if _, ok := ParseRetryAfterHeader(""); ok {
		t.Fatal("empty header parsed")
	}
}
