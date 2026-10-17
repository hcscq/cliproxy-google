package home

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/agversion"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/tidwall/gjson"
)

// Import health check (导入体检): verifies a newly bought credential right after import
// so problems can be claimed with the supplier: subscription tier (loadCodeAssist),
// quota freshness, model catalog, and one real minimal generateContent through the
// credential's pinned egress (never without one) to surface "needs verification",
// 403 terms/permission and 429. Raw upstream error text is kept as evidence.

// Health check categories (a passed check has none).
const (
	HealthNeedsVerify = "needs_verify" // 需验证 (VALIDATION_REQUIRED / verify / onboarding)
	HealthTier        = "tier"         // 非 Pro (tier differs from expected)
	HealthQuotaUsed   = "quota_used"   // 额度非新
	HealthQuotaNone   = "quota_unknown"
	HealthNoCatalog   = "no_catalog"
	HealthTOS         = "tos"        // 403 账号停用/违反条款
	HealthPermission  = "permission" // 403 无权限
	HealthRateLimited = "rate_limited"
	HealthAuth        = "auth" // 401 / no token
	HealthNoEgress    = "no_egress"
	HealthError       = "error"
)

// HealthProbeResult is the raw outcome of the upstream calls of one check.
type HealthProbeResult struct {
	NoEgress        bool   `json:"no_egress,omitempty"`
	TierStatus      int    `json:"tier_status,omitempty"`
	Tier            string `json:"tier,omitempty"`
	TierName        string `json:"tier_name,omitempty"`
	IneligibleCode  string `json:"ineligible_code,omitempty"`
	IneligibleText  string `json:"ineligible_text,omitempty"`
	TierError       string `json:"tier_error,omitempty"`
	Model           string `json:"model,omitempty"`
	GenStatus       int    `json:"gen_status,omitempty"`
	GenBody         string `json:"gen_body,omitempty"`
	GenError        string `json:"gen_error,omitempty"`
	GenLatencyMilli int64  `json:"gen_latency_ms,omitempty"`
}

// HealthInputs are the non-upstream facts of a check (from the store).
type HealthInputs struct {
	QuotaWindows int
	QuotaMin     float64 // min remaining ratio over the credential's quota windows
	CatalogOK    bool
	CatalogCount int
}

// HealthVerdict is the classified outcome.
type HealthVerdict struct {
	Status     string   `json:"status"` // pass | fail
	Categories []string `json:"categories"`
	Reasons    []string `json:"reasons"`  // concise Chinese reasons
	Evidence   string   `json:"evidence"` // raw upstream error text (first failing call)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func tierMatches(tier, expected string) bool {
	if expected == "" {
		return true
	}
	return reserveTierClass(tier) == expected
}

// ClassifyHealth turns probe + inputs into a verdict (pure, tested).
func ClassifyHealth(p HealthProbeResult, in HealthInputs, cfg config.ImportCheckSettings) HealthVerdict {
	v := HealthVerdict{}
	add := func(cat, reason, evidence string) {
		for _, c := range v.Categories {
			if c == cat {
				return
			}
		}
		v.Categories = append(v.Categories, cat)
		v.Reasons = append(v.Reasons, reason)
		if v.Evidence == "" && evidence != "" {
			v.Evidence = clip(evidence, 4000)
		}
	}
	if p.NoEgress {
		add(HealthNoEgress, "未固定出口，未发起上游请求", "")
	}
	// Tier / onboarding (loadCodeAssist).
	low := strings.ToLower(p.IneligibleCode + " " + p.IneligibleText)
	switch {
	case p.IneligibleCode != "" && (strings.Contains(low, "validation") || strings.Contains(low, "verif")):
		add(HealthNeedsVerify, "账号需验证（"+p.IneligibleCode+"）", p.IneligibleText)
	case p.TierStatus == http.StatusUnauthorized:
		add(HealthAuth, "令牌无效（401）", p.TierError)
	case p.TierStatus == http.StatusForbidden:
		classify403(p.TierError, add)
	case p.TierStatus != 0 && p.TierStatus != http.StatusOK:
		add(HealthError, "订阅查询失败（HTTP "+strconv.Itoa(p.TierStatus)+"）", p.TierError)
	case p.TierStatus == http.StatusOK && p.Tier == "":
		add(HealthNeedsVerify, "无订阅层级（需完成开通/验证）", p.IneligibleText)
	case p.Tier != "" && !tierMatches(p.Tier, cfg.ExpectedTier):
		add(HealthTier, "订阅为 "+p.Tier+"，期望 "+cfg.ExpectedTier, "")
	}
	// Real minimal request.
	body := p.GenBody
	lowBody := strings.ToLower(body)
	switch {
	case p.NoEgress:
	case p.GenError != "":
		add(HealthError, "测试请求失败："+clip(p.GenError, 120), p.GenError)
	case p.GenStatus >= 200 && p.GenStatus < 300:
	case strings.Contains(lowBody, "validation_required") || strings.Contains(lowBody, "verify your account") || strings.Contains(lowBody, "verification"):
		add(HealthNeedsVerify, "账号需验证（测试请求 "+strconv.Itoa(p.GenStatus)+"）", body)
	case p.GenStatus == http.StatusForbidden:
		classify403(body, add)
	case p.GenStatus == http.StatusTooManyRequests:
		if strings.Contains(body, "QUOTA_EXHAUSTED") {
			add(HealthQuotaUsed, "测试请求额度耗尽（429）", body)
		} else {
			add(HealthRateLimited, "测试请求被限流（429）", body)
		}
	case p.GenStatus == http.StatusUnauthorized:
		add(HealthAuth, "令牌无效（401）", body)
	case p.GenStatus == http.StatusBadRequest && (strings.Contains(lowBody, "onboard") || strings.Contains(lowBody, "failed_precondition")):
		add(HealthNeedsVerify, "账号未完成开通（400）", body)
	default:
		add(HealthError, "测试请求 HTTP "+strconv.Itoa(p.GenStatus), body)
	}
	// Quota freshness and catalog (skipped when the account is unusable anyway).
	if !p.NoEgress {
		switch {
		case in.QuotaWindows == 0:
			add(HealthQuotaNone, "未取得额度数据", "")
		case in.QuotaMin < cfg.QuotaMin:
			add(HealthQuotaUsed, "额度非新：最低剩余 "+strconv.Itoa(int(in.QuotaMin*100+0.5))+"%", "")
		}
		if !in.CatalogOK || in.CatalogCount == 0 {
			add(HealthNoCatalog, "未取得模型目录", "")
		}
	}
	if len(v.Categories) == 0 {
		v.Status = "pass"
		v.Categories = []string{}
		v.Reasons = []string{}
	} else {
		v.Status = "fail"
	}
	return v
}

func classify403(body string, add func(cat, reason, evidence string)) {
	low := strings.ToLower(body)
	switch {
	case strings.Contains(low, "validation_required") || strings.Contains(low, "verif"):
		add(HealthNeedsVerify, "账号需验证（403）", body)
	case strings.Contains(low, "terms of service") || strings.Contains(low, "has been disabled") || strings.Contains(low, "suspended") || strings.Contains(low, "violat"):
		add(HealthTOS, "账号停用/违反条款（403）", body)
	default:
		add(HealthPermission, "无权限（403）", body)
	}
}

// HealthProbe runs the upstream part of a check for one credential: loadCodeAssist and
// one generateContent with maxOutputTokens 1, both through the pinned egress only.
func (r *Runtime) HealthProbe(ctx context.Context, id, model string) HealthProbeResult {
	out := HealthProbeResult{Model: model}
	full := r.fullAuth(ctx, id)
	if full == nil {
		out.GenError = "credential not found"
		return out
	}
	if strings.TrimSpace(full.ProxyURL) == "" {
		out.NoEgress = true // fail closed: never test through the node IP
		return out
	}
	if extractAccessToken(full) == "" {
		out.GenError = "no access token"
		return out
	}
	r.healthLoadCodeAssist(ctx, full, &out)
	started := time.Now()
	code, _, body, err := sendQuotaProbe(ctx, full, model)
	out.GenLatencyMilli = time.Since(started).Milliseconds()
	out.GenStatus = code
	if err != nil {
		out.GenError = err.Error()
	} else if code < 200 || code >= 300 {
		out.GenBody = clip(string(body), 8000)
	}
	r.recordHealthCheckUsage(full, model, started, code, body, err)
	return out
}

func (r *Runtime) healthLoadCodeAssist(ctx context.Context, full *coreauth.Auth, out *HealthProbeResult) {
	cctx, cancel := context.WithTimeout(ctx, subscriptionCallTimeout)
	defer cancel()
	reqBody := []byte(`{"metadata":{"ide_type":"ANTIGRAVITY","ide_version":"` + agversion.Version() + `","ide_name":"antigravity"}}`)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, subscriptionLoadURL, bytes.NewReader(reqBody))
	if err != nil {
		out.TierError = err.Error()
		return
	}
	req.Header.Set("Authorization", "Bearer "+extractAccessToken(full))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", agversion.UserAgent()+" google-api-nodejs-client/10.3.0")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	resp, err := credentialHTTPClient(full, subscriptionCallTimeout).Do(req)
	if err != nil {
		out.TierError = err.Error()
		return
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	out.TierStatus = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		out.TierError = clip(string(data), 8000)
		return
	}
	out.Tier, out.TierName, _ = parseSubscriptionTier(data)
	for _, t := range gjson.GetBytes(data, "ineligibleTiers").Array() {
		if c := t.Get("reasonCode").String(); c != "" {
			out.IneligibleCode = c
			out.IneligibleText = clip(t.Get("reasonMessage").String()+" "+t.Get("validationUrl").String(), 2000)
			if strings.Contains(strings.ToUpper(c), "VALIDATION") {
				break
			}
		}
	}
}

func (r *Runtime) recordHealthCheckUsage(a *coreauth.Auth, model string, started time.Time, code int, body []byte, callErr error) {
	failed := !(code >= 200 && code < 300)
	payload := map[string]any{
		"usage_id":      "health-" + a.ID + "-" + strconv.FormatInt(started.UnixNano(), 36),
		"request_id":    "health-" + strconv.FormatInt(started.UnixNano(), 36),
		"timestamp":     started.UTC().Format(time.RFC3339Nano),
		"event_type":    "health_check",
		"provider":      "antigravity",
		"executor_type": "ImportCheck",
		"model":         model,
		"alias":         model,
		"auth_index":    a.ID,
		"auth_id":       a.ID,
		"source":        a.Label,
		"latency_ms":    time.Since(started).Milliseconds(),
		"failed":        failed,
	}
	if failed {
		b := clip(string(body), 2000)
		if callErr != nil {
			b = "health check: " + callErr.Error()
		}
		payload["fail"] = map[string]any{"status_code": code, "body": b}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = r.PersistClusterUsagePayload(cctx, string(raw), time.Now().UTC())
}

// ImportCheckSettings returns the current import health check settings.
func (r *Runtime) ImportCheckSettings() config.ImportCheckSettings {
	if r.reserveCfgOverride != nil {
		return r.reserveCfgOverride.ImportCheckSettings()
	}
	return r.Config().ImportCheckSettings()
}
