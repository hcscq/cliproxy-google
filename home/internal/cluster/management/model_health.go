package management

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

// Model health view: one row per Antigravity model combining the upstream
// catalog (deprecations, tags, per-account quota), live per-credential model
// state (cooldowns, last errors), the latest manual probe (stored in the
// database, shared by all replicas) and whether the model is hidden through
// oauth-excluded-models. Nothing is hidden automatically: hidden models are
// listed too.

const (
	modelStatusAvailable = "available"
	modelStatusPartial   = "partial"
	modelStatusCooling   = "cooling"
	modelStatusRejected  = "rejected"
	modelStatusRetired   = "retired"
	modelStatusRemoved   = "removed"
	modelStatusUnknown   = "unknown"
)

type modelHealthCredential struct {
	Label          string     `json:"label"`
	State          string     `json:"state"` // ok | cooling | error | unknown
	HTTPStatus     int        `json:"http_status,omitempty"`
	Message        string     `json:"message,omitempty"`
	NextRetryAfter *time.Time `json:"next_retry_after,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	InCatalog      bool       `json:"in_catalog"`
	QuotaRemaining *float64   `json:"quota_remaining,omitempty"`
	QuotaResetTime string     `json:"quota_reset_time,omitempty"`
	RemovedAt      string     `json:"removed_at,omitempty"`
}

type modelHealthCheck struct {
	Outcome    string             `json:"outcome"`
	HTTPStatus int                `json:"http_status,omitempty"`
	Message    string             `json:"message,omitempty"`
	CheckedAt  time.Time          `json:"checked_at"`
	Details    []home.ProbeResult `json:"details,omitempty"`
}

type modelHealthEntry struct {
	ID             string                  `json:"id"`
	DisplayName    string                  `json:"display_name,omitempty"`
	Status         string                  `json:"status"`
	Reason         string                  `json:"reason,omitempty"`
	Hidden         bool                    `json:"hidden"`
	InCatalog      bool                    `json:"in_catalog"`
	Registered     bool                    `json:"registered"`
	TagTitle       string                  `json:"tag_title,omitempty"`
	TagDescription string                  `json:"tag_description,omitempty"`
	DeprecatedTo   string                  `json:"deprecated_to,omitempty"`
	Credentials    []modelHealthCredential `json:"credentials"`
	LastCheck      *modelHealthCheck       `json:"last_check,omitempty"`
	LastError      string                  `json:"last_error,omitempty"`
	LastErrorAt    *time.Time              `json:"last_error_at,omitempty"`
	Notes          []string                `json:"notes,omitempty"`
}

// GetModelHealth returns the model health view.
func (h *Handler) GetModelHealth(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	entries, meta, err := h.buildModelHealth(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "model_health_failed", err)
		return
	}
	resp := gin.H{"models": entries, "generated_at": time.Now().UTC()}
	for k, v := range meta {
		resp[k] = v
	}
	c.JSON(http.StatusOK, resp)
}

// RetestModelHealth probes one model with every enabled Antigravity credential
// and stores the result.
func (h *Handler) RetestModelHealth(c *gin.Context) {
	var body struct {
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	model := strings.TrimSpace(body.Model)
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime unavailable"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	results := h.runtime.ProbeAntigravityModel(c.Request.Context(), model)
	if len(results) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no enabled antigravity credential"})
		return
	}
	agg := aggregateProbe(results)
	details, _ := json.Marshal(results)
	record := cluster.ModelHealthCheckRecord{Model: model, Outcome: agg.Outcome, HTTPStatus: agg.HTTPStatus, Message: agg.Message, Details: string(details), CheckedAt: time.Now().UTC()}
	if err := h.repo.UpsertModelHealthCheck(ctx, record); err != nil {
		respondError(c, http.StatusInternalServerError, "model_health_store_failed", err)
		return
	}
	entries, _, err := h.buildModelHealth(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "model_health_failed", err)
		return
	}
	var entry *modelHealthEntry
	for i := range entries {
		if entries[i].ID == model {
			entry = &entries[i]
		}
	}
	c.JSON(http.StatusOK, gin.H{"result": checkFromRecord(record), "model": entry})
}

var probeOutcomeRank = map[string]int{
	home.ProbeRetired: 6, home.ProbeRejected: 5, home.ProbeQuota: 4, home.ProbeCapacity: 3, home.ProbeAuthError: 2, home.ProbeError: 1,
}

// aggregateProbe: any credential OK means OK; otherwise the most telling failure.
func aggregateProbe(results []home.ProbeResult) home.ProbeResult {
	best := home.ProbeResult{Outcome: home.ProbeError}
	for _, r := range results {
		if r.Outcome == home.ProbeOK {
			return r
		}
		if probeOutcomeRank[r.Outcome] >= probeOutcomeRank[best.Outcome] {
			best = r
		}
	}
	return best
}

func checkFromRecord(rec cluster.ModelHealthCheckRecord) *modelHealthCheck {
	check := &modelHealthCheck{Outcome: rec.Outcome, HTTPStatus: rec.HTTPStatus, Message: rec.Message, CheckedAt: rec.CheckedAt}
	if strings.TrimSpace(rec.Details) != "" {
		_ = json.Unmarshal([]byte(rec.Details), &check.Details)
	}
	return check
}

var switchTargetPattern = regexp.MustCompile(`(?i)switch to (.+?)(?: in the| instead|\.\s|\.$|$)`)

func (h *Handler) buildModelHealth(ctx context.Context) ([]modelHealthEntry, gin.H, error) {
	meta := gin.H{}
	var catalogs []home.CredentialCatalogHealth
	var excluded []string
	if h.runtime != nil {
		catalogs = h.runtime.AntigravityCatalogHealth()
		if cfg := h.runtime.Config(); cfg != nil {
			excluded = cfg.OAuthExcludedModels["antigravity"]
		}
	}
	auths, err := h.repo.ListAuths(ctx)
	if err != nil {
		return nil, meta, err
	}
	checks, err := h.repo.ListModelHealthChecks(ctx)
	if err != nil {
		return nil, meta, err
	}
	var agAuths []*coreauth.Auth
	for _, a := range auths {
		if a != nil && strings.EqualFold(a.Provider, "antigravity") && !a.Disabled {
			agAuths = append(agAuths, a)
		}
	}
	sort.Slice(agAuths, func(i, j int) bool { return credLabel(agAuths[i]) < credLabel(agAuths[j]) })
	// Some transitions (e.g. model_not_supported suspensions) live only in the
	// dispatching runtime's memory; merge them with the stored state.
	memStates := map[string]map[string]*coreauth.ModelState{}
	if h.runtime != nil && h.runtime.CoreManager() != nil {
		for _, a := range h.runtime.CoreManager().List() {
			if a != nil && len(a.ModelStates) > 0 {
				memStates[a.ID] = a.ModelStates
			}
		}
	}

	ids := map[string]string{} // lower -> canonical
	addID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, "*?") {
			return
		}
		if _, ok := ids[strings.ToLower(id)]; !ok {
			ids[strings.ToLower(id)] = id
		}
	}
	registered := map[string]bool{}
	reg := registry.GetGlobalRegistry()
	for _, a := range agAuths {
		for _, m := range reg.GetModelsForClient(a.ID) {
			if m != nil {
				addID(m.ID)
				registered[strings.ToLower(m.ID)] = true
			}
		}
	}
	allDisplay := map[string]bool{}
	var latestFetch time.Time
	for _, cat := range catalogs {
		if cat.FetchedAt.After(latestFetch) {
			latestFetch = cat.FetchedAt
		}
		for _, m := range cat.Models {
			addID(m.ID)
			if m.DisplayName != "" {
				allDisplay[strings.ToLower(m.DisplayName)] = true
			}
		}
	}
	hidden := map[string]bool{}
	for _, e := range excluded {
		addID(e)
		hidden[strings.ToLower(strings.TrimSpace(e))] = true
	}
	for id := range checks {
		addID(id)
	}
	// Models that disappeared from a credential's upstream catalog recently.
	removedBy := map[string]map[string]string{} // lower model -> auth id -> time
	if h.runtime != nil {
		for _, st := range h.runtime.AntigravityCatalogStatuses() {
			for _, ch := range st.Removed {
				addID(ch.Model)
				k := strings.ToLower(ch.Model)
				if removedBy[k] == nil {
					removedBy[k] = map[string]string{}
				}
				removedBy[k][st.AuthID] = ch.At
			}
		}
	}
	if !latestFetch.IsZero() {
		meta["catalog_fetched_at"] = latestFetch
	}

	now := time.Now()
	var missingTargets []string
	out := make([]modelHealthEntry, 0, len(ids))
	for key, id := range ids {
		e := modelHealthEntry{ID: id, Hidden: hidden[key] || matchesExcluded(excluded, id), Registered: registered[key]}
		catalogCount := 0
		for _, cat := range catalogs {
			if m, ok := cat.Models[key]; ok {
				catalogCount++
				e.InCatalog = true
				if e.DisplayName == "" {
					e.DisplayName = m.DisplayName
				}
				if e.TagTitle == "" {
					e.TagTitle, e.TagDescription = m.TagTitle, m.TagDescription
				}
			}
			if to, ok := cat.Deprecated[key]; ok && e.DeprecatedTo == "" {
				e.DeprecatedTo = to
				if to == "" {
					e.DeprecatedTo = "-"
				}
			}
		}
		var lastSuccess time.Time
		available, cooling, errored := 0, 0, 0
		var earliestRetry *time.Time
		retiredMsg := ""
		removedCount := 0
		for _, a := range agAuths {
			cred := modelHealthCredential{Label: credLabel(a), State: "ok"}
			for _, cat := range catalogs {
				if cat.AuthID == a.ID {
					if m, ok := cat.Models[key]; ok {
						cred.InCatalog = true
						cred.QuotaRemaining, cred.QuotaResetTime = m.QuotaRemaining, m.QuotaResetTime
					}
				}
			}
			if at, ok := removedBy[key][a.ID]; ok && !cred.InCatalog {
				cred.State, cred.RemovedAt = "removed", at
			}
			st := a.ModelStates[id]
			if mem := memStates[a.ID][id]; mem != nil && (st == nil || mem.UpdatedAt.After(st.UpdatedAt)) {
				st = mem
			}
			if st != nil {
				if !st.UpdatedAt.IsZero() {
					t := st.UpdatedAt
					cred.UpdatedAt = &t
				}
				if st.Status == coreauth.StatusError {
					cred.State = "error"
					cred.Message = st.StatusMessage
					if st.LastError != nil {
						cred.HTTPStatus = st.LastError.HTTPStatus
					}
					if st.Unavailable && st.NextRetryAfter.After(now) {
						cred.State = "cooling"
						t := st.NextRetryAfter
						cred.NextRetryAfter = &t
						if earliestRetry == nil || t.Before(*earliestRetry) {
							earliestRetry = &t
						}
					}
					if e.LastErrorAt == nil || st.UpdatedAt.After(*e.LastErrorAt) {
						t := st.UpdatedAt
						e.LastErrorAt, e.LastError = &t, st.StatusMessage
					}
					if strings.Contains(st.StatusMessage, "MODEL_RETIRED") {
						retiredMsg = retiredUpstreamMessage(st.StatusMessage)
					}
				} else if st.UpdatedAt.After(lastSuccess) {
					lastSuccess = st.UpdatedAt
				}
			}
			switch cred.State {
			case "removed":
				removedCount++
			case "cooling":
				cooling++
			case "error":
				errored++
				available++
			default:
				available++
			}
			e.Credentials = append(e.Credentials, cred)
		}
		if rec, ok := checks[id]; ok {
			e.LastCheck = checkFromRecord(rec)
		} else if rec, ok := checks[key]; ok {
			e.LastCheck = checkFromRecord(rec)
		}
		// Persist a retirement seen only in this replica's memory so every
		// replica (and later restarts) shows it.
		if retiredMsg != "" && e.LastErrorAt != nil && (e.LastCheck == nil || e.LastCheck.CheckedAt.Before(*e.LastErrorAt)) {
			rec := cluster.ModelHealthCheckRecord{Model: id, Outcome: home.ProbeRetired, HTTPStatus: http.StatusNotFound, Message: retiredMsg, CheckedAt: e.LastErrorAt.UTC()}
			if h.repo.UpsertModelHealthCheck(ctx, rec) == nil {
				e.LastCheck = checkFromRecord(rec)
			}
		}
		// A probe failure older than a later successful request is stale.
		check := e.LastCheck
		if check != nil && check.Outcome != home.ProbeOK && !lastSuccess.IsZero() && lastSuccess.After(check.CheckedAt) {
			check = nil
		}
		_ = errored
		switch {
		case check != nil && check.Outcome == home.ProbeRetired:
			e.Status, e.Reason = modelStatusRetired, "已下线（上游）："+check.Message
			retiredMsg = check.Message
		case retiredMsg != "":
			e.Status, e.Reason = modelStatusRetired, "已下线（上游）："+retiredMsg
		case e.DeprecatedTo != "":
			e.Status = modelStatusRejected
			e.Reason = "上游已弃用此模型"
			if e.DeprecatedTo != "-" {
				e.Reason += "，建议改用 " + e.DeprecatedTo
			}
			if check != nil && check.Outcome == home.ProbeRejected {
				e.Reason += "（上游拒绝 " + itoa(check.HTTPStatus) + "）"
			}
		case check != nil && check.Outcome == home.ProbeRejected:
			e.Status, e.Reason = modelStatusRejected, "上游拒绝 ("+itoa(check.HTTPStatus)+")："+check.Message
		case removedCount > 0 && !e.InCatalog:
			e.Status, e.Reason = modelStatusRemoved, "上游已从模型目录移除"
			if at := latestChange(removedBy[key]); at != "" {
				e.Reason += "（" + at + "）"
			}
		case len(agAuths) > 0 && cooling == len(agAuths):
			e.Status, e.Reason = modelStatusCooling, "所有凭据冷却中"
			if earliestRetry != nil {
				e.Reason += "，最早恢复于 " + earliestRetry.In(shanghai()).Format("01-02 15:04")
			}
		case cooling > 0:
			e.Status, e.Reason = modelStatusPartial, "部分凭据冷却（"+itoa(available)+"/"+itoa(len(agAuths))+" 可用）"
		case !e.InCatalog && !e.Registered:
			e.Status, e.Reason = modelStatusUnknown, "当前账号的模型目录中没有此模型"
		default:
			e.Status = modelStatusAvailable
			if check != nil && check.Outcome == home.ProbeCapacity {
				e.Reason = "最近一次测试：上游容量不足 (503)，可稍后重试"
			} else if check != nil && check.Outcome == home.ProbeQuota {
				e.Reason = "最近一次测试：额度/限流 (429)"
			}
		}
		if e.Status == modelStatusRetired {
			if m := switchTargetPattern.FindStringSubmatch(retiredMsg); len(m) == 2 {
				target := strings.TrimSpace(m[1])
				if target != "" && !allDisplay[strings.ToLower(target)] {
					note := "上游提示切换到 " + target + "，但当前账号的模型目录中没有该模型"
					e.Notes = append(e.Notes, note)
					missingTargets = appendUnique(missingTargets, target)
				}
			}
		}
		if e.Status != modelStatusRetired {
			e.Notes = append(e.Notes, modelCapabilityNotes(e)...)
		}
		if e.Credentials == nil {
			e.Credentials = []modelHealthCredential{}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(missingTargets) > 0 {
		meta["missing_switch_targets"] = missingTargets
	}
	return out, meta, nil
}

// modelCapabilityNotes returns known per-model behaviour notes (Chinese).
func modelCapabilityNotes(e modelHealthEntry) []string {
	id := strings.ToLower(e.ID)
	var notes []string
	if (strings.HasPrefix(id, "gemini-3") || id == "gemini-pro-agent") && !strings.Contains(id, "image") {
		notes = append(notes, "流式思考文本不稳定（上游限制）：上游流式只在思考较长时才返回思考摘要，思考签名在正文之后才到；非流式总是完整。可在“设置 → 流式思考模拟”中开启（默认关闭）。")
	}
	if strings.HasPrefix(id, "gpt-oss") {
		notes = append(notes, "仅支持文本输入（不支持图片）。")
	}
	if strings.EqualFold(e.TagTitle, "Leaving Soon") {
		n := "上游标记“即将下线”"
		if e.TagDescription != "" {
			n += "：" + e.TagDescription
		}
		notes = append(notes, n)
	}
	return notes
}

func matchesExcluded(patterns []string, id string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, strings.ToLower(id)); ok {
			return true
		}
	}
	return false
}

func credLabel(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if email, ok := a.Metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email)
	}
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func shanghai() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

func itoa(n int) string { return strconv.Itoa(n) }

// retiredUpstreamMessage extracts the upstream notice from a stored
// MODEL_RETIRED error body; falls back to the raw text.
func retiredUpstreamMessage(raw string) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &body) != nil {
		return raw
	}
	for _, d := range body.Error.Details {
		if m := strings.TrimSpace(d.Metadata["upstream_message"]); m != "" {
			return m
		}
	}
	if m := strings.TrimSpace(body.Error.Message); m != "" {
		return m
	}
	return raw
}

// latestChange formats the most recent time in a model's per-credential change map.
func latestChange(m map[string]string) string {
	var latest time.Time
	for _, at := range m {
		if t, err := time.Parse(time.RFC3339, at); err == nil && t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.In(shanghai()).Format("01-02 15:04")
}
