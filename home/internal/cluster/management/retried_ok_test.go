package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestEventsMarkRetriedOKAndFinalFailedFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, closeRepo := newUsageObservabilityTestHandler(t)
	defer closeRepo()
	ctx := context.Background()
	for _, p := range []string{
		// request A: 502 on cred-1, then 200 on cred-2
		`{"timestamp":"2026-06-10T01:00:00Z","provider":"antigravity","model":"m","request_id":"req-A","auth_index":"cred-1","failed":true,"fail":{"status_code":502,"body":"gfe 502"},"latency_ms":45000}`,
		`{"timestamp":"2026-06-10T01:00:45Z","provider":"antigravity","model":"m","request_id":"req-A","auth_index":"cred-2","latency_ms":3000,"tokens":{"output_tokens":5,"total_tokens":5}}`,
		// request B: final failure
		`{"timestamp":"2026-06-10T01:01:00Z","provider":"antigravity","model":"m","request_id":"req-B","auth_index":"cred-3","failed":true,"fail":{"status_code":429,"body":"quota"}}`,
	} {
		if _, err := handler.repo.AppendUsage(ctx, p, "192.0.2.10"); err != nil {
			t.Fatal(err)
		}
	}
	engine := gin.New()
	engine.GET("/request-events", handler.ListRequestEvents)
	get := func(q string) []map[string]any {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/request-events?limit=50&from=2026-06-10T00:00:00Z&to=2026-06-10T02:00:00Z"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var out struct{ Items []map[string]any }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Items
	}
	items := get("")
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	marked := 0
	for _, it := range items {
		ro, ok := it["retried_ok"].(map[string]any)
		if !ok {
			continue
		}
		marked++
		if it["request_id"] != "req-A" || ro["auth_index"] != "cred-2" {
			t.Fatalf("wrong annotation: %v", it)
		}
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1", marked)
	}
	if f := get("&status=final_failed"); len(f) != 1 || f[0]["request_id"] != "req-B" {
		t.Fatalf("final_failed = %v", f)
	}
	if f := get("&status=retried_ok"); len(f) != 1 || f[0]["request_id"] != "req-A" {
		t.Fatalf("retried_ok = %v", f)
	}
}
