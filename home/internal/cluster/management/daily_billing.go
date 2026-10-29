package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/dailybilling"
	log "github.com/sirupsen/logrus"
)

const dailyBillingCostKey = "management/daily-billing/cost"

type dailyCostView struct {
	dailybilling.CostSettings
	Source         string `json:"source"`
	CostConfigured bool   `json:"cost_configured"`
}

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
		if h.repo == nil {
			h.dailyBilling = dailybilling.NewService(dailybilling.LoadConfig(""), nil)
			return
		}
		h.dailyBilling = dailybilling.NewService(dailybilling.LoadConfig(""), h.repo.ReadOnlyDB())
	})
	return h.dailyBilling
}

// GetDailyBillingSettings returns the effective per-credential cost basis.
func (h *Handler) GetDailyBillingSettings(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	c.JSON(http.StatusOK, h.dailyCostView(ctx))
}

// PutDailyBillingSettings stores the panel cost basis. Zero costs hide the cost columns.
func (h *Handler) PutDailyBillingSettings(c *gin.Context) {
	var body dailybilling.CostSettings
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		respondError(c, http.StatusBadRequest, "invalid_body", errors.New("成本参数无效"))
		return
	}
	if errValidate := body.Validate(); errValidate != nil {
		respondError(c, http.StatusBadRequest, "invalid_cost", errors.New("成本参数无效"))
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	if errSave := h.saveDailyCost(ctx, body); errSave != nil {
		log.Warnf("daily billing cost settings save failed: %s", errSave.Error())
		respondError(c, http.StatusInternalServerError, "daily_billing_cost_failed", errors.New("成本配置保存失败"))
		return
	}
	c.JSON(http.StatusOK, costView(body, "panel"))
}

// GetDailyBilling reports estimated spend by day, subscription tier, and model.
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
	if settings, found, errLoad := h.loadDailyCost(ctx); errLoad != nil {
		log.Warnf("daily billing cost settings unreadable: %s", errLoad.Error())
	} else if found {
		query.Cost = &settings
	}
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

func (h *Handler) dailyCostView(ctx context.Context) dailyCostView {
	settings, found, errLoad := h.loadDailyCost(ctx)
	if errLoad != nil {
		log.Warnf("daily billing cost settings unreadable: %s", errLoad.Error())
		found = false
	}
	if found {
		return costView(settings, "panel")
	}
	return costView(h.fileDailyCost(), "config")
}

func (h *Handler) fileDailyCost() dailybilling.CostSettings {
	reporter := h.dailyBillingReporter()
	type fileCost interface {
		FileCost() dailybilling.CostSettings
	}
	if source, ok := reporter.(fileCost); ok {
		return source.FileCost()
	}
	return dailybilling.CostSettings{}
}

func costView(settings dailybilling.CostSettings, source string) dailyCostView {
	return dailyCostView{CostSettings: settings, Source: source, CostConfigured: settings.Configured()}
}

func (h *Handler) loadDailyCost(ctx context.Context) (dailybilling.CostSettings, bool, error) {
	if h == nil {
		return dailybilling.CostSettings{}, false, nil
	}
	if h.repo != nil {
		raw, found, errGet := h.repo.KVGet(ctx, dailyBillingCostKey)
		if errGet != nil || !found {
			return dailybilling.CostSettings{}, false, errGet
		}
		settings, errDecode := dailybilling.DecodeCostSettings(raw)
		if errDecode != nil {
			return dailybilling.CostSettings{}, false, errDecode
		}
		return settings, true, nil
	}
	h.dailyCostMu.Lock()
	defer h.dailyCostMu.Unlock()
	if !h.dailyCostSet {
		return dailybilling.CostSettings{}, false, nil
	}
	return h.dailyCostMem, true, nil
}

func (h *Handler) saveDailyCost(ctx context.Context, settings dailybilling.CostSettings) error {
	if h == nil {
		return errors.New("handler is nil")
	}
	if h.repo != nil {
		raw, errEncode := dailybilling.EncodeCostSettings(settings)
		if errEncode != nil {
			return errEncode
		}
		_, errSet := h.repo.KVSet(ctx, dailyBillingCostKey, raw, 0, cluster.KVSetModeAlways)
		return errSet
	}
	h.dailyCostMu.Lock()
	h.dailyCostMem = settings
	h.dailyCostSet = true
	h.dailyCostMu.Unlock()
	return nil
}

func (h *Handler) dailyBillingContext(c *gin.Context) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		parent = c.Request.Context()
	}
	return context.WithTimeout(parent, 30*time.Second)
}
