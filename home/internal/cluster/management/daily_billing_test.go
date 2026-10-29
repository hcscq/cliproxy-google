package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
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

func TestDailyBillingSettingsRoundTripAndReportOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubDailyBilling{report: dailybilling.Report{Configured: true, Days: []dailybilling.Day{}, Totals: dailybilling.Totals{}}}
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	handler.SetDailyBilling(stub)

	get := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(get)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily/settings", nil)
	handler.GetDailyBillingSettings(getCtx)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"source":"config"`) {
		t.Fatalf("get settings = %d %s", get.Code, get.Body.String())
	}

	body := []byte(`{"pro_monthly_cost":31,"ultra_daily_cost":2,"month_days":30}`)
	put := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(put)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/billing/daily/settings", bytes.NewReader(body))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	handler.PutDailyBillingSettings(putCtx)
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"source":"panel"`) || !strings.Contains(put.Body.String(), `"cost_configured":true`) {
		t.Fatalf("put settings = %d %s", put.Code, put.Body.String())
	}

	bad := httptest.NewRecorder()
	badCtx, _ := gin.CreateTestContext(bad)
	badCtx.Request = httptest.NewRequest(http.MethodPut, "/billing/daily/settings", bytes.NewBufferString(`{"pro_monthly_cost":-1}`))
	badCtx.Request.Header.Set("Content-Type", "application/json")
	handler.PutDailyBillingSettings(badCtx)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad cost status = %d", bad.Code)
	}

	report := httptest.NewRecorder()
	reportCtx, _ := gin.CreateTestContext(report)
	reportCtx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily?from=2026-10-27&to=2026-10-27&timezone=UTC", nil)
	handler.GetDailyBilling(reportCtx)
	if report.Code != http.StatusOK {
		t.Fatalf("report status = %d %s", report.Code, report.Body.String())
	}
	if stub.query.Cost == nil || stub.query.Cost.ProMonthlyCost != 31 || stub.query.Cost.UltraDailyCost != 2 || stub.query.Cost.MonthDays != 30 {
		t.Fatalf("query cost = %+v", stub.query.Cost)
	}
}

func TestDailyBillingSettingsPersistInKV(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	db, errOpen := cluster.OpenSQLite(ctx, filepath.Join(t.TempDir(), "home.db"))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	sqlDB, errSQL := db.DB()
	if errSQL != nil {
		t.Fatal(errSQL)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if errMigrate := cluster.AutoMigrate(db); errMigrate != nil {
		t.Fatal(errMigrate)
	}
	handler := NewHandler(cluster.NewRepository(db), nil, "127.0.0.1", 0)
	handler.SetDailyBilling(&stubDailyBilling{})

	put := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(put)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/billing/daily/settings", bytes.NewBufferString(`{"ultra_monthly_cost":90,"month_days":30}`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	handler.PutDailyBillingSettings(putCtx)
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d %s", put.Code, put.Body.String())
	}

	other := NewHandler(cluster.NewRepository(db), nil, "127.0.0.1", 0)
	other.SetDailyBilling(&stubDailyBilling{})
	get := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(get)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/billing/daily/settings", nil)
	other.GetDailyBillingSettings(getCtx)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"ultra_monthly_cost":90`) || !strings.Contains(get.Body.String(), `"source":"panel"`) {
		t.Fatalf("reloaded settings = %d %s", get.Code, get.Body.String())
	}
}
