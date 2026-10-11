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

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	log "github.com/sirupsen/logrus"
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
	antigravityDynamicRefreshInterval = 3 * time.Hour
	antigravityDynamicRetryInterval   = 5 * time.Minute
	antigravityDefaultModelsBaseURL   = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelsEndpointPath     = "/v1internal:fetchAvailableModels"
	antigravityModelsUserAgent        = "antigravity/1.11.5 windows/amd64"
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
	failedAt  time.Time
	inFlight  bool
	tokenHash string
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
		}
		add(m)
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
	for _, id := range extraIDs {
		add(&ModelInfo{ID: id, Object: "model", Created: now, OwnedBy: "antigravity", Type: "antigravity", DisplayName: id, Name: "models/" + id, UserDefined: true})
	}
	return out
}

func (r *Runtime) cachedAntigravityCatalog(a *coreauth.Auth) map[string]antigravityUpstreamModel {
	token := extractAccessToken(a)
	antigravityCatalogMu.Lock()
	entry := antigravityCatalog[a.ID]
	if entry == nil {
		entry = &antigravityCatalogEntry{}
		antigravityCatalog[a.ID] = entry
	}
	now := time.Now()
	stale := entry.models == nil || now.Sub(entry.fetchedAt) > antigravityDynamicRefreshInterval
	retryOK := now.Sub(entry.failedAt) > antigravityDynamicRetryInterval || entry.tokenHash != token
	models := entry.models
	// In cluster mode the in-memory auth is a minimal index without secrets;
	// the refresh goroutine loads the full credential from PostgreSQL.
	if stale && retryOK && !entry.inFlight {
		entry.inFlight = true
		entry.tokenHash = token
		go r.refreshAntigravityCatalog(a.ID, a.Clone())
	}
	antigravityCatalogMu.Unlock()
	return models
}

func (r *Runtime) refreshAntigravityCatalog(authID string, snapshot *coreauth.Auth) {
	auth := snapshot
	if r != nil && r.coreManager != nil {
		if current, ok := r.coreManager.GetByID(authID); ok && current != nil {
			auth = current
		}
	}
	var models map[string]antigravityUpstreamModel
	var err error
	if auth != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		full := auth
		if extractAccessToken(full) == "" && r != nil && r.clusterAdapter != nil && r.clusterAdapter.Enabled() {
			if loaded, errLoad := r.clusterAdapter.GetFullAuth(ctx, authID); errLoad == nil && loaded != nil {
				full = loaded
			} else if errLoad != nil {
				err = errLoad
			}
		}
		if err == nil {
			models, err = fetchAntigravityCatalog(ctx, full)
		}
		cancel()
	}
	antigravityCatalogMu.Lock()
	entry := antigravityCatalog[authID]
	if entry == nil {
		entry = &antigravityCatalogEntry{}
		antigravityCatalog[authID] = entry
	}
	entry.inFlight = false
	changed := false
	if auth == nil && err == nil {
		err = antigravityStringError("credential not found")
	}
	if err != nil {
		entry.failedAt = time.Now()
	} else {
		changed = !sameAntigravityCatalog(entry.models, models)
		entry.models = models
		entry.fetchedAt = time.Now()
	}
	antigravityCatalogMu.Unlock()
	if err != nil {
		log.Warnf("antigravity model catalog fetch failed for %s: %v", authID, err)
		return
	}
	if changed && auth != nil {
		log.Infof("antigravity model catalog for %s: %d upstream models", authID, len(models))
		r.registerModelsForAuth(auth)
		if r.coreManager != nil {
			r.coreManager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			r.coreManager.RefreshSchedulerEntry(auth.ID)
		}
	}
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
	req.Header.Set("User-Agent", antigravityModelsUserAgent)
	resp, err := antigravityHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &antigravityHTTPError{status: resp.StatusCode}
	}
	return parseAntigravityCatalog(data)
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
		}
		_ = json.Unmarshal(raw, &meta)
		out[strings.ToLower(id)] = antigravityUpstreamModel{ID: id, DisplayName: strings.TrimSpace(meta.DisplayName), MaxTokens: meta.MaxTokens, MaxOutput: meta.MaxOutputTokens,
			SupportsThinking: meta.SupportsThinking, MinThinking: meta.MinThinkingBudget, ThinkingBudget: meta.ThinkingBudget}
	}
	return out, nil
}

type antigravityHTTPError struct{ status int }

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
