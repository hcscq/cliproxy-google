package home

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/agversion"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

// Dynamic Antigravity model catalog.
//
// The static catalog (models.json) only lists models the upstream project knows
// about, so models Antigravity serves to a given account but that are missing
// from the static list were never exposed. Home now asks the upstream
// fetchAvailableModels endpoint with each credential and merges the result:
//   - static models that the account can actually use keep their rich metadata;
//   - models only present upstream are synthesized from the upstream entry;
//   - HOME_ANTIGRAVITY_EXTRA_MODELS (comma list) and per-credential
//     metadata "home_config_models" add models by configuration;
//   - oauth-model-alias (existing config) adds aliases on top.
// Set HOME_ANTIGRAVITY_DYNAMIC_MODELS=false to fall back to the static list.

const (
	antigravityDefaultModelsBaseURL = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelsEndpointPath   = "/v1internal:fetchAvailableModels"
)

type antigravityUpstreamModel struct {
	ID          string
	DisplayName string
	MaxTokens   int
	MaxOutput   int
	// Optional thinking hints from the catalog (absent on many entries).
	SupportsThinking *bool
	MinThinking      int
	ThinkingBudget   int
}

type antigravityCatalogEntry struct {
	models    map[string]antigravityUpstreamModel // nil = unknown
	fetchedAt time.Time
	health    *antigravityCatalogHealth
}

var (
	antigravityCatalogMu sync.Mutex
	antigravityCatalog   = map[string]*antigravityCatalogEntry{}
	// antigravityHTTPClient is replaceable in tests.
	antigravityHTTPClient = &http.Client{Timeout: 30 * time.Second}
	antigravityModelsURL  = antigravityDefaultModelsBaseURL + antigravityModelsEndpointPath
)

func antigravityDynamicModelsEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HOME_ANTIGRAVITY_DYNAMIC_MODELS")))
	return v != "false" && v != "0" && v != "off"
}

func antigravityExtraModelIDs() []string {
	var out []string
	for _, id := range strings.Split(os.Getenv("HOME_ANTIGRAVITY_EXTRA_MODELS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// antigravityModelsForAuth returns the merged model list for one credential and
// schedules a background catalog refresh when needed.
// antigravityAccountGatedPrefixes are static models Google serves only to some
// accounts (gemini-4-argon-*: Ultra only). Without a known catalog for the
// credential they are not registered, so a cold catalog cache never routes
// them to an account that would 404.
var antigravityAccountGatedPrefixes = []string{"gemini-4-argon"}

func antigravityAccountGatedModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range antigravityAccountGatedPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func (r *Runtime) antigravityModelsForAuth(a *coreauth.Auth, configModels []*ModelInfo) []*ModelInfo {
	static := registry.GetAntigravityModels()
	var upstream map[string]antigravityUpstreamModel
	if antigravityDynamicModelsEnabled() && a != nil {
		upstream = r.cachedAntigravityCatalog(a)
	}
	return mergeAntigravityModels(static, upstream, configModels, antigravityExtraModelIDs())
}

func mergeAntigravityModels(static []*ModelInfo, upstream map[string]antigravityUpstreamModel, configModels []*ModelInfo, extraIDs []string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(static)+len(upstream)+len(configModels)+len(extraIDs))
	seen := map[string]struct{}{}
	add := func(m *ModelInfo) {
		if m == nil {
			return
		}
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	for _, m := range static {
		if m == nil {
			continue
		}
		if upstream != nil {
			if _, ok := upstream[strings.ToLower(strings.TrimSpace(m.ID))]; !ok {
				continue // the account cannot use this model
			}
		} else if antigravityAccountGatedModel(m.ID) {
			continue // catalog not known yet: never assume a tier-gated model
		}
		add(withAntigravityOffMode(m))
	}
	ids := make([]string, 0, len(upstream))
	for id := range upstream {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := time.Now().Unix()
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		u := upstream[id]
		info := &ModelInfo{
			ID:          u.ID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "antigravity",
			Type:        "antigravity",
			DisplayName: u.DisplayName,
			Name:        "models/" + u.ID,
		}
		if info.DisplayName == "" {
			info.DisplayName = u.ID
		}
		if u.MaxTokens > 0 {
			info.InputTokenLimit = u.MaxTokens
			info.ContextLength = u.MaxTokens
		}
		if u.MaxOutput > 0 {
			info.OutputTokenLimit = u.MaxOutput
			info.MaxCompletionTokens = u.MaxOutput
		}
		info.Thinking = antigravityDynamicThinking(u)
		if strings.Contains(id, "gemini") {
			info.SupportedGenerationMethods = []string{"generateContent", "countTokens", "streamGenerateContent"}
		}
		add(info)
	}
	for _, m := range configModels {
		add(m)
	}
	byID := make(map[string]*ModelInfo, len(out))
	for _, m := range out {
		byID[strings.ToLower(strings.TrimSpace(m.ID))] = m
	}
	for _, id := range extraIDs {
		add(antigravityExtraModel(id, byID, now))
	}
	return out
}

// antigravityExtraNote marks models served by ID although the upstream catalog
// does not list them (HOME_ANTIGRAVITY_EXTRA_MODELS); shown in the panel.
const antigravityExtraNote = "Upstream accepts this ID (verified); not listed in the Antigravity catalog."

// antigravityExtraModel builds an extra model entry. The ID is passed upstream
// unchanged. When a catalog sibling exists (gemini-3.8-flash-high ->
// gemini-3.8-flash-tiered) it copies that sibling's limits and thinking
// metadata so the model behaves like its catalog family; otherwise thinking
// passes through unvalidated.
func antigravityExtraModel(id string, known map[string]*ModelInfo, now int64) *ModelInfo {
	info := &ModelInfo{ID: id, Object: "model", Created: now, OwnedBy: "antigravity", Type: "antigravity",
		DisplayName: id, Name: "models/" + id, Description: antigravityExtraNote}
	sib := antigravityExtraSibling(id, known)
	if sib == nil {
		info.UserDefined = true
		return info
	}
	info.InputTokenLimit, info.ContextLength = sib.InputTokenLimit, sib.ContextLength
	info.OutputTokenLimit, info.MaxCompletionTokens = sib.OutputTokenLimit, sib.MaxCompletionTokens
	info.SupportedGenerationMethods = append([]string(nil), sib.SupportedGenerationMethods...)
	if sib.Thinking != nil {
		t := *sib.Thinking
		t.Levels = append([]string(nil), sib.Thinking.Levels...)
		info.Thinking = &t
	}
	return withAntigravityOffMode(info)
}

// antigravityExtraSibling finds the catalog model of the same family: the ID
// with its last "-suffix" replaced by "-tiered", or the bare family ID.
func antigravityExtraSibling(id string, known map[string]*ModelInfo) *ModelInfo {
	key := strings.ToLower(strings.TrimSpace(id))
	i := strings.LastIndex(key, "-")
	if i <= 0 {
		return nil
	}
	base := key[:i]
	for _, cand := range []string{base + "-tiered", base} {
		if m := known[cand]; m != nil {
			return m
		}
	}
	return nil
}

func (r *Runtime) cachedAntigravityCatalog(a *coreauth.Auth) map[string]antigravityUpstreamModel {
	antigravityCatalogMu.Lock()
	entry := antigravityCatalog[a.ID]
	var models map[string]antigravityUpstreamModel
	if entry != nil {
		models = entry.models
	}
	antigravityCatalogMu.Unlock()
	if models == nil && r != nil && r.agCatalog.kick.CompareAndSwap(false, true) {
		// Unknown credential (new or first start): run one sync pass now
		// instead of waiting for the next tick.
		go func() {
			defer r.agCatalog.kick.Store(false)
			r.antigravityCatalogTick(context.Background(), "startup")
		}()
	}
	return models
}

func sameAntigravityCatalog(a, b map[string]antigravityUpstreamModel) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fetchAntigravityCatalog(ctx context.Context, auth *coreauth.Auth) (map[string]antigravityUpstreamModel, error) {
	data, err := fetchAntigravityCatalogRaw(ctx, auth)
	if err != nil {
		return nil, err
	}
	return parseAntigravityCatalog(data)
}

func fetchAntigravityCatalogRaw(ctx context.Context, auth *coreauth.Auth) ([]byte, error) {
	token := extractAccessToken(auth)
	if token == "" {
		return nil, errAntigravityNoToken
	}
	project, _ := auth.Metadata["project_id"].(string)
	body, _ := json.Marshal(map[string]string{"project": project})
	url := antigravityModelsURL
	if base, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(base) != "" {
		url = strings.TrimRight(strings.TrimSpace(base), "/") + antigravityModelsEndpointPath
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", agversion.UserAgent())
	resp, err := credentialHTTPClient(auth, 30*time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &antigravityHTTPError{status: resp.StatusCode, body: clip(string(data), 4000)}
	}
	return data, nil
}

func parseAntigravityCatalog(data []byte) (map[string]antigravityUpstreamModel, error) {
	var parsed struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	out := make(map[string]antigravityUpstreamModel, len(parsed.Models))
	for id, raw := range parsed.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		var meta struct {
			DisplayName       string `json:"displayName"`
			MaxTokens         int    `json:"maxTokens"`
			MaxOutputTokens   int    `json:"maxOutputTokens"`
			SupportsThinking  *bool  `json:"supportsThinking"`
			MinThinkingBudget int    `json:"minThinkingBudget"`
			ThinkingBudget    int    `json:"thinkingBudget"`
			Disabled          bool   `json:"disabled"`
		}
		_ = json.Unmarshal(raw, &meta)
		if meta.Disabled {
			// Upstream lists some models as unavailable placeholders for this
			// account (e.g. "gemini-4-argon-disabled" for Pro with the hub UA);
			// they are not callable, so never register them.
			continue
		}
		out[strings.ToLower(id)] = antigravityUpstreamModel{ID: id, DisplayName: strings.TrimSpace(meta.DisplayName), MaxTokens: meta.MaxTokens, MaxOutput: meta.MaxOutputTokens,
			SupportsThinking: meta.SupportsThinking, MinThinking: meta.MinThinkingBudget, ThinkingBudget: meta.ThinkingBudget}
	}
	return out, nil
}

type antigravityHTTPError struct {
	status int
	body   string
}

func (e *antigravityHTTPError) Error() string {
	return "fetchAvailableModels returned HTTP " + http.StatusText(e.status)
}

type antigravityStringError string

func (e antigravityStringError) Error() string { return string(e) }

const errAntigravityNoToken = antigravityStringError("credential has no access token")

// antigravityDynamicThinking derives thinking capabilities for a model that is
// only known from the upstream catalog. Without them the proxy treats the model
// as "no thinking support" and strips reasoning_effort / thinking /
// thinkingConfig from every request. Order: an explicit catalog
// supportsThinking=false; a static definition with the same id (e.g. the Gemini
// API entry for gemini-2.5-flash); catalog budget hints; Gemini family defaults.
func antigravityDynamicThinking(u antigravityUpstreamModel) *registry.ThinkingSupport {
	t := antigravityDynamicThinkingBase(u)
	if t != nil && t.OffMode == "" {
		t.OffMode = antigravityThinkingOffMode(u.ID)
	}
	return t
}

// withAntigravityOffMode returns m with its measured thinking off mode attached,
// copying instead of mutating the shared static definition.
func withAntigravityOffMode(m *ModelInfo) *ModelInfo {
	if m == nil || m.Thinking == nil || m.Thinking.OffMode != "" {
		return m
	}
	mode := antigravityThinkingOffMode(m.ID)
	if mode == "" {
		return m
	}
	cp := *m
	t := *m.Thinking
	t.OffMode = mode
	cp.Thinking = &t
	return &cp
}

// antigravityThinkingOffMode is how "thinking off" works per Antigravity model,
// measured with raw upstream calls (2026-10-11, thinkingBudget=0 vs default):
//   - zero:         0 accepted, no thought tokens        (3.1-flash-lite, 3.5-flash-low/extra-low, 3-flash-agent)
//   - omit:         0 rejected (400), no thinking by default (3.5-flash-lite)
//   - minimal:      0 rejected (400), minimal ≈ 0 thoughts (3-flash)
//   - zero-reduced: 0 accepted, still ~35-160 thought tokens (3.1-pro-low, 3.6-flash-*, 3.8-flash, gemini-pro-agent)
//
// Unlisted models keep the proxy's generic fallback (lowest level, reported).
func antigravityThinkingOffMode(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	switch id {
	case "gemini-3.1-flash-lite", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low", "gemini-3-flash-agent":
		return "zero"
	case "gemini-3.5-flash-lite":
		return "omit"
	case "gemini-3-flash":
		return "minimal"
	case "gemini-3.1-pro-low", "gemini-pro-agent":
		return "zero-reduced"
	}
	if strings.HasPrefix(id, "gemini-3.6-flash") || strings.HasPrefix(id, "gemini-3.8-flash") {
		return "zero-reduced"
	}
	return ""
}

func antigravityDynamicThinkingBase(u antigravityUpstreamModel) *registry.ThinkingSupport {
	if u.SupportsThinking != nil && !*u.SupportsThinking {
		return nil
	}
	if static := registry.LookupStaticModelInfo(u.ID); static != nil && static.Thinking != nil {
		t := *static.Thinking
		t.Levels = append([]string(nil), static.Thinking.Levels...)
		return &t
	}
	id := strings.ToLower(u.ID)
	maxBudget := 24576
	if u.MaxOutput > 0 && u.MaxOutput-1 < maxBudget {
		maxBudget = u.MaxOutput - 1
	}
	switch {
	case strings.HasPrefix(id, "gemini-2.5"):
		minBudget := 128
		if u.MinThinking > 0 {
			minBudget = u.MinThinking
		}
		// Flash and Flash-Lite can turn thinking off; Pro cannot.
		return &registry.ThinkingSupport{Min: minBudget, Max: maxBudget, ZeroAllowed: !strings.Contains(id, "pro"), DynamicAllowed: true}
	case strings.HasPrefix(id, "gemini-3") || strings.HasPrefix(id, "gemini-pro") || strings.HasPrefix(id, "gemini-flash"):
		if u.MaxOutput > 0 {
			maxBudget = u.MaxOutput - 1
		} else {
			maxBudget = 32768
		}
		return &registry.ThinkingSupport{Min: 128, Max: maxBudget, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}}
	case u.SupportsThinking != nil && *u.SupportsThinking:
		minBudget := u.MinThinking
		if minBudget <= 0 {
			minBudget = 1
		}
		return &registry.ThinkingSupport{Min: minBudget, Max: maxBudget, DynamicAllowed: true}
	}
	return nil
}
