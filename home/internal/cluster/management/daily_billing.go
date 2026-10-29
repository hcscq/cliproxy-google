package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/dailybilling"
)

// SetDailyBilling replaces the daily billing reporter. Tests use this to avoid a database.
func (h *Handler) SetDailyBilling(reporter dailybilling.Reporter) {
	if h == nil {
		return
	}
	h.dailyBilling = reporter
}

func (h *Handler) dailyBillingReporter() dailybilling.Reporter {
	if h == nil {
		return dailybilling.NewService(dailybilling.Config{}, nil)
	}
	if h.dailyBilling != nil {
		return h.dailyBilling
	}
	h.dailyBillingOnce.Do(func() {
		if h.dailyBilling != nil {
			return
		}
		h.dailyBilling = dailybilling.NewService(dailybilling.LoadConfig(""), h.repo.ReadOnlyDB())
	})
	return h.dailyBilling
}

// GetDailyBilling reports estimated gemini-4 spend by day, tier, and variant.
func (h *Handler) GetDailyBilling(c *gin.Context) {
	reporter := h.dailyBillingReporter()
	timezone := strings.TrimSpace(c.Query("timezone"))
	if timezone == "" {
		timezone = reporter.DefaultTimezone()
	}
	now := time.Now()
	if h != nil && h.dailyBillingNow != nil {
		now = h.dailyBillingNow()
	}
	query, errRange := dailybilling.ResolveRange(now, c.Query("from"), c.Query("to"), timezone)
	if errRange != nil {
		if errors.Is(errRange, dailybilling.ErrInvalidTimezone) {
			respondError(c, http.StatusBadRequest, "invalid_timezone", errors.New("时区无效"))
			return
		}
		respondError(c, http.StatusBadRequest, "invalid_time_range", errors.New("日期范围无效"))
		return
	}

	ctx, cancel := h.dailyBillingContext(c)
	defer cancel()
	report, errReport := reporter.Report(ctx, query)
	if errReport != nil {
		if errors.Is(errReport, dailybilling.ErrSourceUnavailable) {
			respondError(c, http.StatusServiceUnavailable, "new_api_unavailable", errors.New("new-api 数据源不可用"))
			return
		}
		respondError(c, http.StatusInternalServerError, "daily_billing_failed", errors.New("每日计费查询失败"))
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h *Handler) dailyBillingContext(c *gin.Context) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		parent = c.Request.Context()
	}
	return context.WithTimeout(parent, 30*time.Second)
}
