package home

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/agversion"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Model health support: the upstream catalog carries per-model state that the
// management panel surfaces (deprecations with a replacement model, "Leaving
// Soon" tags, per-account quota), and ProbeAntigravityModel sends one tiny real
// request per credential so operators can retest a model on demand.

// CatalogModelHealth is the health-relevant part of one catalog entry.
type CatalogModelHealth struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name,omitempty"`
	TagTitle       string   `json:"tag_title,omitempty"`
	TagDescription string   `json:"tag_description,omitempty"`
	QuotaRemaining *float64 `json:"quota_remaining,omitempty"`
	QuotaResetTime string   `json:"quota_reset_time,omitempty"`
	DeprecatedTo   string   `json:"deprecated_to,omitempty"`
}

// CredentialCatalogHealth is the catalog view of one credential.
type CredentialCatalogHealth struct {
	AuthID    string                        `json:"auth_id"`
	Label     string                        `json:"label"`
	FetchedAt time.Time                     `json:"fetched_at"`
	Models    map[string]CatalogModelHealth `json:"models"`
	// Deprecated maps a deprecated model id to its replacement (may also be
	// absent from Models).
	Deprecated map[string]string `json:"deprecated"`
}

type antigravityCatalogHealth struct {
	fetchedAt  time.Time
	models     map[string]CatalogModelHealth
	deprecated map[string]string
}

func parseAntigravityCatalogHealth(data []byte) *antigravityCatalogHealth {
	out := &antigravityCatalogHealth{models: map[string]CatalogModelHealth{}, deprecated: map[string]string{}}
	root := gjson.ParseBytes(data)
	root.Get("models").ForEach(func(key, value gjson.Result) bool {
		id := strings.TrimSpace(key.String())
		if id == "" {
			return true
		}
		m := CatalogModelHealth{
			ID:             id,
			DisplayName:    strings.TrimSpace(value.Get("displayName").String()),
			TagTitle:       strings.TrimSpace(value.Get("tagTitle").String()),
			TagDescription: strings.TrimSpace(value.Get("tagDescription").String()),
			QuotaResetTime: strings.TrimSpace(value.Get("quotaInfo.resetTime").String()),
		}
		if q := value.Get("quotaInfo.remainingFraction"); q.Exists() {
			f := q.Float()
			m.QuotaRemaining = &f
		} else if value.Get("quotaInfo").Exists() {
			zero := 0.0
			m.QuotaRemaining = &zero
		}
		out.models[strings.ToLower(id)] = m
		return true
	})
	root.Get("deprecatedModelIds").ForEach(func(key, value gjson.Result) bool {
		id := strings.ToLower(strings.TrimSpace(key.String()))
		if id == "" {
			return true
		}
		to := strings.TrimSpace(value.Get("newModelId").String())
		out.deprecated[id] = to
		if m, ok := out.models[id]; ok {
			m.DeprecatedTo = to
			out.models[id] = m
		}
		return true
	})
	return out
}

// AntigravityCatalogHealth returns the cached catalog health per credential.
func (r *Runtime) AntigravityCatalogHealth() []CredentialCatalogHealth {
	labels := map[string]string{}
	if r != nil && r.coreManager != nil {
		for _, a := range r.coreManager.List() {
			// Disabled credentials keep a frozen last-known catalog; counting it
			// would advertise models no usable credential serves.
			if a != nil && !a.Disabled && a.Status != coreauth.StatusDisabled {
				labels[a.ID] = authDisplayLabel(a)
			}
		}
	}
	antigravityCatalogMu.Lock()
	defer antigravityCatalogMu.Unlock()
	out := make([]CredentialCatalogHealth, 0, len(antigravityCatalog))
	for authID, entry := range antigravityCatalog {
		if entry == nil || entry.health == nil {
			continue
		}
		if r != nil && r.coreManager != nil {
			if _, ok := labels[authID]; !ok {
				continue // credential removed or disabled
			}
		}
		item := CredentialCatalogHealth{AuthID: authID, Label: labels[authID], FetchedAt: entry.health.fetchedAt,
			Models: map[string]CatalogModelHealth{}, Deprecated: map[string]string{}}
		for k, v := range entry.health.models {
			item.Models[k] = v
		}
		for k, v := range entry.health.deprecated {
			item.Deprecated[k] = v
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func authDisplayLabel(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if email, ok := a.Metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email)
	}
	if strings.TrimSpace(a.Label) != "" {
		return strings.TrimSpace(a.Label)
	}
	return a.ID
}

// Probe outcomes.
const (
	ProbeOK        = "ok"
	ProbeRetired   = "retired"
	ProbeRejected  = "rejected"
	ProbeQuota     = "quota"
	ProbeCapacity  = "capacity"
	ProbeAuthError = "auth_error"
	ProbeError     = "error"
)

// ProbeResult is the outcome of one probe request.
type ProbeResult struct {
	AuthID     string `json:"auth_id"`
	Label      string `json:"label"`
	Outcome    string `json:"outcome"`
	HTTPStatus int    `json:"http_status"`
	Message    string `json:"message,omitempty"`
	LatencyMs  int64  `json:"latency_ms"`
}

var retiredNoticePattern = regexp.MustCompile(`(?i)\bis no longer available\b[^.]*\.\s*(please\s+)?switch to\b`)

// RetiredNoticeText reports whether text is an upstream "model retired" notice.
func RetiredNoticeText(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && len(t) <= 600 && retiredNoticePattern.MatchString(t)
}

const antigravityGeneratePath = "/v1internal:generateContent"

// ProbeAntigravityModel sends one minimal request for model with every enabled
// Antigravity credential and classifies each answer.
func (r *Runtime) ProbeAntigravityModel(ctx context.Context, model string) []ProbeResult {
	model = strings.TrimSpace(model)
	var results []ProbeResult
	if r == nil || r.coreManager == nil || model == "" {
		return results
	}
	auths := r.coreManager.List()
	sort.Slice(auths, func(i, j int) bool { return authDisplayLabel(auths[i]) < authDisplayLabel(auths[j]) })
	for _, a := range auths {
		if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "antigravity") {
			continue
		}
		full := a
		if extractAccessToken(full) == "" && r.clusterAdapter != nil && r.clusterAdapter.Enabled() {
			if loaded, err := r.clusterAdapter.GetFullAuth(ctx, a.ID); err == nil && loaded != nil {
				full = loaded
			}
		}
		res := probeAntigravity(ctx, full, model)
		res.AuthID, res.Label = a.ID, authDisplayLabel(a)
		results = append(results, res)
	}
	return results
}

func probeAntigravity(ctx context.Context, auth *coreauth.Auth, model string) ProbeResult {
	token := extractAccessToken(auth)
	if token == "" {
		return ProbeResult{Outcome: ProbeAuthError, Message: "credential has no access token"}
	}
	project, _ := auth.Metadata["project_id"].(string)
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	body, _ := json.Marshal(map[string]any{
		"project":   project,
		"model":     model,
		"userAgent": "antigravity",
		"requestId": "probe-" + hex.EncodeToString(idBytes),
		"request": map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Reply with the single word OK."}}}},
		},
	})
	base := strings.TrimSuffix(antigravityModelsURL, antigravityModelsEndpointPath)
	if b, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
		base = strings.TrimRight(strings.TrimSpace(b), "/")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+antigravityGeneratePath, bytes.NewReader(body))
	if err != nil {
		return ProbeResult{Outcome: ProbeError, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", agversion.UserAgent())
	start := time.Now()
	resp, err := antigravityProbeHTTPClient.Do(req)
	if err != nil {
		return ProbeResult{Outcome: ProbeError, Message: err.Error(), LatencyMs: time.Since(start).Milliseconds()}
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	res := ClassifyProbeResponse(resp.StatusCode, data)
	res.LatencyMs = time.Since(start).Milliseconds()
	return res
}

// antigravityProbeHTTPClient is replaceable in tests.
var antigravityProbeHTTPClient = &http.Client{Timeout: 90 * time.Second}

// ClassifyProbeResponse maps an upstream answer to a probe outcome.
func ClassifyProbeResponse(status int, body []byte) ProbeResult {
	res := ProbeResult{HTTPStatus: status}
	root := gjson.ParseBytes(body)
	if status >= 200 && status < 300 {
		var text strings.Builder
		parts := root.Get("response.candidates.0.content.parts")
		if !parts.Exists() {
			parts = root.Get("candidates.0.content.parts")
		}
		for _, p := range parts.Array() {
			if !p.Get("thought").Bool() {
				text.WriteString(p.Get("text").String())
			}
		}
		if RetiredNoticeText(text.String()) {
			res.Outcome, res.Message = ProbeRetired, strings.TrimSpace(text.String())
			return res
		}
		res.Outcome = ProbeOK
		if mv := root.Get("response.modelVersion").String(); mv != "" {
			res.Message = "modelVersion=" + mv
		}
		return res
	}
	msg := strings.TrimSpace(root.Get("error.message").String())
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	res.Message = msg
	lower := strings.ToLower(string(body))
	switch {
	case status == http.StatusUnauthorized:
		res.Outcome = ProbeAuthError
	case status == http.StatusTooManyRequests:
		res.Outcome = ProbeQuota
	case status == http.StatusServiceUnavailable && (strings.Contains(lower, "capacity") || strings.Contains(lower, "unavailable")):
		res.Outcome = ProbeCapacity
	case status == http.StatusBadRequest, status == http.StatusNotFound, status == http.StatusForbidden, status == http.StatusUnprocessableEntity:
		res.Outcome = ProbeRejected
	default:
		res.Outcome = ProbeError
	}
	return res
}
