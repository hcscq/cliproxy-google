package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/dailybilling"
)

type stubDailyBilling struct {
	report dailybilling.Report
	err    error
	query  dailybilling.Query
	tz     string
}

func (s *stubDailyBilling) Report(_ context.Context, query dailybilling.Query) (dailybilling.Report, error) {
	s.query = query
	return s.report, s.err
}

func (s *stubDailyBilling) DefaultTimezone() string {
	if s.tz == "" {
		return "UTC"
	}
	return s.tz
}

func TestGetDailyBillingUnconfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	handler.SetDailyBilling(dailybilling.NewService(dailybilling.Config{}, nil))

	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily", nil)
	handler.GetDailyBilling(ctx)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var report dailybilling.Report
	if errDecode := json.Unmarshal(resp.Body.Bytes(), &report); errDecode != nil {
		t.Fatal(errDecode)
	}
	if report.Configured || report.Message != dailybilling.UnconfiguredMessage {
		t.Fatalf("report = %+v", report)
	}
	if strings.Contains(resp.Body.String(), "password") {
		t.Fatalf("body leaked a secret field: %s", resp.Body.String())
	}
}

func TestGetDailyBillingRejectsBadRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	handler.SetDailyBilling(&stubDailyBilling{})

	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily?from=2026-10-28&to=2026-10-01&timezone=UTC", nil)
	handler.GetDailyBilling(ctx)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestGetDailyBillingPassesRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubDailyBilling{report: dailybilling.Report{
		Configured: true,
		Estimated:  true,
		Days:       []dailybilling.Day{},
		Totals:     dailybilling.Totals{USD: 1.5, ProRatio: 0.25},
	}}
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	handler.SetDailyBilling(stub)
	handler.dailyBillingNow = func() time.Time { return time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC) }

	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily?from=2026-10-27&to=2026-10-28&timezone=Asia/Shanghai", nil)
	handler.GetDailyBilling(ctx)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if stub.query.FromDate != "2026-10-27" || stub.query.ToDate != "2026-10-28" || stub.query.Timezone != "Asia/Shanghai" {
		t.Fatalf("query = %+v", stub.query)
	}
	if !strings.Contains(resp.Body.String(), `"estimated":true`) || !strings.Contains(resp.Body.String(), `"usd":1.5`) {
		t.Fatalf("body = %s", resp.Body.String())
	}
}
