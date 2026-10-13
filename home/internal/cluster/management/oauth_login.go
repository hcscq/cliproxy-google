package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/auth/antigravity"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	maxOAuthStateLength = 128

	anthropicAuthURL     = "https://claude.ai/oauth/authorize"
	anthropicRedirectURI = "http://localhost:54545/callback"

	codexAuthURL     = "https://auth.openai.com/oauth/authorize"
	codexRedirectURI = "http://localhost:1455/auth/callback"

	antigravityAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	antigravityTokenEndpoint    = "https://oauth2.googleapis.com/token"
	antigravityUserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
	antigravityCallbackPort     = 51121
	antigravityAPIEndpoint      = "https://cloudcode-pa.googleapis.com"
	antigravityAPIVersion       = "v1internal"
	antigravityFallbackVersion  = "1.21.9"
	antigravityNodeAPIClientUA  = "google-api-nodejs-client/10.3.0"
	antigravityGoogAPIClientUA  = "gl-node/22.21.1"
)

var (
	errInvalidOAuthState   = errors.New("invalid oauth state")
	errUnsupportedProvider = errors.New("unsupported oauth provider")
	antigravityScopes      = []string{"https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile", "https://www.googleapis.com/auth/cclog", "https://www.googleapis.com/auth/experimentsandconfigs"}
)

type pkceCodes struct {
	CodeVerifier  string
	CodeChallenge string
}

type oauthCallbackRequest struct {
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
	Code        string `json:"code"`
	State       string `json:"state"`
	Error       string `json:"error"`
}

// RequestAntigravityToken handles request antigravity token.
func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	state, errState := generateOAuthState("agv")
	if errState != nil {
		log.Errorf("cluster oauth: failed to generate antigravity state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravityCallbackPort)
	authURL := buildAntigravityAuthURL(state, redirectURI)
	if errRegister := h.registerOAuthSession(c, "antigravity", state, map[string]any{
		"redirect_uri":  redirectURI,
		"egress_id":     strings.TrimSpace(c.Query("egress_id")),
		"allow_primary": strings.TrimSpace(c.Query("allow_primary")),
	}); errRegister != nil {
		respondError(c, http.StatusInternalServerError, "oauth_session_failed", errRegister)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

// GetAuthStatus returns an auth status.
func (h *Handler) GetAuthStatus(c *gin.Context) {
	// Validate request inputs before mutating persisted state.
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if errState := validateOAuthState(state); errState != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	session, errSession := h.repo.GetOAuthSession(ctx, state)
	if errSession != nil {
		respondError(c, http.StatusInternalServerError, "oauth_session_failed", errSession)
		return
	}
	if session == nil {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if strings.EqualFold(session.Status, "complete") {
		c.JSON(http.StatusOK, completedOAuthResponse(session, false))
		return
	}
	if strings.EqualFold(session.Status, "error") {
		errMsg := strings.TrimSpace(session.Error)
		if errMsg == "" {
			errMsg = "Authentication failed"
		}
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": errMsg})
		return
	}
	data, errData := cluster.OAuthSessionData(session)
	if errData != nil {
		respondError(c, http.StatusInternalServerError, "oauth_session_failed", errData)
		return
	}
	if h.respondPluginAuthStatus(c, ctx, session, data) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

// handleOAuthCallback handles an o auth callback.
func (h *Handler) handleOAuthCallback(c *gin.Context) {
	// Resolve credential context before calling upstream OAuth services.
	var req oauthCallbackRequest
	if c.Request.Method == http.MethodGet {
		req.State = c.Query("state")
		req.Code = c.Query("code")
		req.Error = c.Query("error")
		if req.Error == "" {
			req.Error = c.Query("error_description")
		}
		req.Provider = c.Query("provider")
	} else if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	state := strings.TrimSpace(req.State)
	code := strings.TrimSpace(req.Code)
	errorMessage := strings.TrimSpace(req.Error)
	if rawRedirect := strings.TrimSpace(req.RedirectURL); rawRedirect != "" {
		parsedURL, errParse := url.Parse(rawRedirect)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid redirect_url"})
			return
		}
		query := parsedURL.Query()
		if state == "" {
			state = strings.TrimSpace(query.Get("state"))
		}
		if code == "" {
			code = strings.TrimSpace(query.Get("code"))
		}
		if errorMessage == "" {
			errorMessage = strings.TrimSpace(query.Get("error"))
			if errorMessage == "" {
				errorMessage = strings.TrimSpace(query.Get("error_description"))
			}
		}
	}

	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state is required"})
		return
	}
	if errState := validateOAuthState(state); errState != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	if code == "" && errorMessage == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "code or error is required"})
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	session, errSession := h.repo.GetOAuthSession(ctx, state)
	if errSession != nil {
		respondError(c, http.StatusInternalServerError, "oauth_session_failed", errSession)
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "unknown or expired state",
			"message": "登录会话不存在或已过期（有效期 20 分钟），请重新生成登录链接后再粘贴回调地址"})
		return
	}
	if strings.EqualFold(session.Status, "complete") {
		// Idempotent: the same callback was already processed (possibly by another Home replica).
		c.JSON(http.StatusOK, completedOAuthResponse(session, true))
		return
	}
	if strings.TrimSpace(session.Status) != "" {
		errMsg := strings.TrimSpace(session.Error)
		if errMsg == "" {
			errMsg = "oauth flow is not pending"
		}
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": errMsg})
		return
	}
	data, errData := cluster.OAuthSessionData(session)
	if errData != nil {
		respondError(c, http.StatusInternalServerError, "oauth_session_failed", errData)
		return
	}

	providerInput := strings.TrimSpace(req.Provider)
	if providerInput == "" {
		providerInput = strings.TrimSpace(session.Provider)
	}
	isPlugin := oauthSessionIsPlugin(data)
	var provider string
	var errProvider error
	if isPlugin {
		provider, errProvider = normalizePluginOAuthProvider(providerInput)
	} else {
		provider, errProvider = normalizeOAuthProvider(providerInput)
	}
	if errProvider != nil || (!isPlugin && (provider == "kimi" || provider == "kimi-ai" || provider == "meta")) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unsupported provider"})
		return
	}
	if !strings.EqualFold(session.Provider, provider) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "provider does not match state"})
		return
	}

	if errorMessage != "" {
		if errMerge := h.repo.MergeOAuthSessionData(ctx, state, map[string]any{
			"callback_received_at": time.Now().UTC().Format(time.RFC3339),
			"callback_error":       errorMessage,
		}); errMerge != nil {
			respondOAuthSessionMergeError(c, errMerge)
			return
		}
		if errSet := h.repo.SetOAuthSessionError(ctx, state, "Authentication failed"); errSet != nil {
			respondError(c, http.StatusInternalServerError, "oauth_session_failed", errSet)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	if isPlugin {
		if errMerge := h.repo.MergeOAuthSessionData(ctx, state, pluginOAuthCallbackSessionData(code, provider)); errMerge != nil {
			respondOAuthSessionMergeError(c, errMerge)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	if errMerge := h.repo.MergeOAuthSessionData(ctx, state, map[string]any{
		"callback_received_at": time.Now().UTC().Format(time.RFC3339),
	}); errMerge != nil {
		respondOAuthSessionMergeError(c, errMerge)
		return
	}

	go h.processOAuthCallback(provider, state, code)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func respondOAuthSessionMergeError(c *gin.Context, errMerge error) {
	if errors.Is(errMerge, cluster.ErrOAuthSessionNotPending) {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is not pending"})
		return
	}
	respondError(c, http.StatusInternalServerError, "oauth_session_failed", errMerge)
}

// processOAuthCallback handles a process o auth callback.
func (h *Handler) processOAuthCallback(provider, state, code string) {
	// Resolve credential context before calling upstream OAuth services.
	ctx, cancel := h.oauthSessionContext(context.Background(), state, 5*time.Minute)
	defer cancel()

	session, errSession := h.repo.GetOAuthSession(ctx, state)
	if errSession != nil {
		log.Errorf("cluster oauth: load session failed: %v", errSession)
		return
	}
	if session == nil || session.Status != "" || ctx.Err() != nil {
		return
	}
	data, errData := cluster.OAuthSessionData(session)
	if errData != nil {
		_ = h.repo.SetOAuthSessionError(ctx, state, "Authentication failed")
		log.Errorf("cluster oauth: decode session data failed: %v", errData)
		return
	}

	var errProcess error
	switch provider {
	case "antigravity":
		errProcess = h.exchangeAntigravityCallback(ctx, code, data)
	default:
		errProcess = fmt.Errorf("unsupported provider: %s", provider)
	}
	if errProcess != nil {
		_ = h.repo.SetOAuthSessionError(ctx, state, authStatusMessage(provider, errProcess))
		log.Errorf("cluster oauth: %s callback failed: %v", provider, errProcess)
		return
	}
}

// registerOAuthSession handles a register o auth session.
func (h *Handler) registerOAuthSession(c *gin.Context, provider, state string, data map[string]any) error {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	record, errRecord := cluster.NewOAuthSessionRecord(provider, state, data, time.Now().UTC())
	if errRecord != nil {
		return errRecord
	}
	return h.repo.UpsertOAuthSession(ctx, record)
}

// exchangeAntigravityCallback handles an exchange antigravity callback.
func (h *Handler) exchangeAntigravityCallback(ctx context.Context, code string, data map[string]any) error {
	// Validate request inputs before mutating persisted state.
	redirectURI := stringFromAny(data["redirect_uri"])
	if redirectURI == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth-callback", antigravityCallbackPort)
	}

	// The whole login (token exchange, userinfo, onboarding) already uses the
	// credential's egress, so Google sees one IP from the first call.
	var egress home.EgressSpec
	explicitEgress := false
	if h.runtime != nil {
		choice := stringFromAny(data["egress_id"])
		spec, ok, errEgress := h.runtime.ResolveEgress(ctx, choice, parseAllowPrimary(stringFromAny(data["allow_primary"])))
		if errEgress != nil {
			return errEgress
		}
		if ok {
			egress = spec
			explicitEgress = choice != "" && !strings.EqualFold(choice, home.EgressAuto)
			ctx = withEgressProxy(ctx, spec.ProxyURL)
		}
	}

	form := url.Values{}
	form.Set("code", strings.TrimSpace(code))
	form.Set("client_id", antigravity.ClientID)
	form.Set("client_secret", antigravity.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, antigravityTokenEndpoint, strings.NewReader(form.Encode()))
	if errRequest != nil {
		return errRequest
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, errDo := h.oauthHTTPClientFor(ctx).Do(req)
	if errDo != nil {
		return errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity oauth: response body close error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return errRead
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(body, &tokenResp); errUnmarshal != nil {
		return errUnmarshal
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return fmt.Errorf("token exchange returned empty access token")
	}

	email, errEmail := h.fetchAntigravityEmail(ctx, tokenResp.AccessToken)
	if errEmail != nil {
		return errEmail
	}
	projectID := ""
	if fetchedProjectID, errProject := h.fetchAntigravityProjectID(ctx, tokenResp.AccessToken); errProject != nil {
		log.Warnf("cluster oauth: failed to fetch antigravity project ID: %v", errProject)
	} else {
		projectID = strings.TrimSpace(fetchedProjectID)
	}
	now := time.Now()
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"expires_in":    tokenResp.ExpiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		"email":         email,
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}
	if existing := h.findActiveCredentialByEmail(ctx, "antigravity", email); existing != nil {
		if explicitEgress {
			home.ApplyEgressMetadata(existing, egress)
		}
		return h.refreshExistingOAuthCredential(ctx, existing, metadata)
	}
	if egress.ProxyURL != "" {
		metadata["proxy_url"] = egress.ProxyURL
		metadata[home.EgressIDKey] = egress.Name
		metadata[home.EgressIPKey] = egress.IP
	}
	return h.storeOAuthMetadataWithContext(ctx, metadata, antigravityCredentialFileName(email))
}

// completedOAuthResponse renders a completed session, including its non-secret result.
func completedOAuthResponse(session *cluster.OAuthSessionRecord, alreadyCompleted bool) gin.H {
	resp := gin.H{"status": "ok"}
	if alreadyCompleted {
		resp["already_completed"] = true
	}
	for key, value := range cluster.OAuthSessionResult(session) {
		resp[key] = value
	}
	return resp
}

// findActiveCredentialByEmail returns the oldest active credential of provider with the same email.
func (h *Handler) findActiveCredentialByEmail(ctx context.Context, provider, email string) *coreauth.Auth {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	auths, errList := h.repo.ListAuths(ctx)
	if errList != nil {
		log.Warnf("cluster oauth: dedupe lookup failed: %v", errList)
		return nil
	}
	var found *coreauth.Auth
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(auth.Provider, provider) {
			continue
		}
		candidate := strings.ToLower(strings.TrimSpace(stringFromAny(auth.Metadata["email"])))
		if candidate == "" {
			candidate = strings.ToLower(strings.TrimSpace(auth.Label))
		}
		if candidate != email {
			continue
		}
		if found == nil || (!auth.CreatedAt.IsZero() && auth.CreatedAt.Before(found.CreatedAt)) {
			found = auth
		}
	}
	return found
}

// refreshExistingOAuthCredential merges fresh tokens into an existing credential instead of
// creating a duplicate. Groups, notes, exclusions and other attributes are preserved.
func (h *Handler) refreshExistingOAuthCredential(ctx context.Context, existing *coreauth.Auth, metadata map[string]any) error {
	if existing.Metadata == nil {
		existing.Metadata = map[string]any{}
	}
	for _, key := range []string{"access_token", "refresh_token", "expires_in", "timestamp", "expired", "project_id"} {
		if value, ok := metadata[key]; ok && value != nil && value != "" {
			existing.Metadata[key] = value
		}
	}
	existing.UpdatedAt = time.Now().UTC()
	result := map[string]any{"credential_id": existing.ID, "email": stringFromAny(metadata["email"]), "updated_existing": true}
	state, _ := ctx.Value(oauthSessionStateKey{}).(string)
	if state != "" {
		if errComplete := h.repo.CompleteOAuthSessionWithAuthsResult(ctx, state, []*coreauth.Auth{existing}, result); errComplete != nil {
			return errComplete
		}
	} else if _, errUpsert := h.repo.UpsertAuth(ctx, existing, "upsert"); errUpsert != nil {
		return errUpsert
	}
	log.Infof("cluster oauth: %s login for existing credential %s refreshed its tokens instead of adding a duplicate", existing.Provider, existing.ID)
	return h.refreshAuths(ctx)
}

// storeOAuthMetadataWithContext stores an o auth metadata with context.
func (h *Handler) storeOAuthMetadataWithContext(ctx context.Context, metadata map[string]any, originalFilename string) error {
	raw, errMarshal := json.MarshalIndent(metadata, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	raw = append(raw, '\n')
	_, errStore := h.storeOAuthPayloadWithContext(ctx, raw, originalFilename)
	return errStore
}

// buildAntigravityAuthURL builds an antigravity auth url.
func buildAntigravityAuthURL(state, redirectURI string) string {
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", antigravity.ClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(antigravityScopes, " "))
	params.Set("state", state)
	return antigravityAuthEndpoint + "?" + params.Encode()
}

// geminiProjectIDFromMap derives gemini project id from map.
func geminiProjectIDFromMap(values map[string]any) string {
	if values == nil {
		return ""
	}
	switch projectValue := values["cloudaicompanionProject"].(type) {
	case string:
		return strings.TrimSpace(projectValue)
	case map[string]any:
		return stringFromAny(projectValue["id"])
	default:
		return ""
	}
}

// fetchAntigravityEmail fetches an antigravity email.
func (h *Handler) fetchAntigravityEmail(ctx context.Context, accessToken string) (string, error) {
	// Validate request inputs before mutating persisted state.
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, antigravityUserInfoEndpoint, nil)
	if errRequest != nil {
		return "", errRequest
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	resp, errDo := h.oauthHTTPClientFor(ctx).Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity userinfo: response body close error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", errRead
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("userinfo request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	email := strings.TrimSpace(gjson.GetBytes(body, "email").String())
	if email == "" {
		return "", fmt.Errorf("userinfo response missing email")
	}
	return email, nil
}

// fetchAntigravityProjectID fetches an antigravity project id.
func (h *Handler) fetchAntigravityProjectID(ctx context.Context, accessToken string) (string, error) {
	// Validate request inputs before mutating persisted state.
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("antigravity project: missing access token")
	}
	userAgent := antigravityLoadCodeAssistUserAgent()
	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": antigravityVersionFromUserAgent(userAgent),
			"ide_name":    "antigravity",
		},
	}
	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}
	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", antigravityAPIEndpoint, antigravityAPIVersion)
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(rawBody))
	if errRequest != nil {
		return "", fmt.Errorf("create request: %w", errRequest)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityGoogAPIClientUA)

	resp, errDo := h.oauthHTTPClientFor(ctx).Do(req)
	if errDo != nil {
		return "", fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: response body close error: %v", errClose)
		}
	}()
	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return "", fmt.Errorf("decode response: %w", errDecode)
	}
	projectID := geminiProjectIDFromMap(loadResp)
	if projectID != "" {
		return projectID, nil
	}

	tierID := "legacy-tier"
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
				if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
					tierID = strings.TrimSpace(id)
					break
				}
			}
		}
	}
	return h.onboardAntigravityUser(ctx, accessToken, tierID)
}

// onboardAntigravityUser handles an onboard antigravity user.
func (h *Handler) onboardAntigravityUser(ctx context.Context, accessToken string, tierID string) (string, error) {
	// Validate request inputs before mutating persisted state.
	if ctx == nil {
		ctx = context.Background()
	}
	userAgent := antigravityLoadCodeAssistUserAgent()
	requestBody := map[string]any{
		"tierId": strings.TrimSpace(tierID),
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": antigravityVersionFromUserAgent(userAgent),
			"ide_name":    "antigravity",
		},
	}
	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reqCtx := ctx
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		reqCtx, cancel := context.WithTimeout(reqCtx, 30*time.Second)
		endpointURL := fmt.Sprintf("%s/%s:onboardUser", antigravityAPIEndpoint, antigravityAPIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(rawBody))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Goog-Api-Client", antigravityGoogAPIClientUA)

		resp, errDo := h.oauthHTTPClientFor(ctx).Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}
		bodyBytes, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity onboardUser: response body close error: %v", errClose)
		}
		cancel()
		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}
		if resp.StatusCode != http.StatusOK {
			responsePreview := strings.TrimSpace(string(bodyBytes))
			if len(responsePreview) > 200 {
				responsePreview = responsePreview[:200]
			}
			return "", fmt.Errorf("http %d: %s", resp.StatusCode, responsePreview)
		}
		var data map[string]any
		if errDecode := json.Unmarshal(bodyBytes, &data); errDecode != nil {
			return "", fmt.Errorf("decode response: %w", errDecode)
		}
		if done, okDone := data["done"].(bool); okDone && done {
			if responseData, okResp := data["response"].(map[string]any); okResp {
				projectID := geminiProjectIDFromMap(responseData)
				if projectID != "" {
					return projectID, nil
				}
			}
			return "", fmt.Errorf("no project_id in response")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", nil
}

// antigravityLoadCodeAssistUserAgent handles an antigravity load code assist user agent.
func antigravityLoadCodeAssistUserAgent() string {
	return fmt.Sprintf("antigravity/%s darwin/arm64 %s", antigravityFallbackVersion, antigravityNodeAPIClientUA)
}

// antigravityVersionFromUserAgent derives antigravity version from user agent.
func antigravityVersionFromUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	lower := strings.ToLower(userAgent)
	if !strings.HasPrefix(lower, "antigravity/") {
		return antigravityFallbackVersion
	}
	rest := userAgent[len("antigravity/"):]
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return antigravityFallbackVersion
	}
	return rest
}

// generatePKCECodes generates a pkce codes.
func generatePKCECodes() (*pkceCodes, error) {
	verifier, errVerifier := randomURLSafe(96)
	if errVerifier != nil {
		return nil, errVerifier
	}
	hash := sha256.Sum256([]byte(verifier))
	return &pkceCodes{
		CodeVerifier:  verifier,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(hash[:]),
	}, nil
}

// generateOAuthState generates an o auth state.
func generateOAuthState(prefix string) (string, error) {
	value, errState := randomURLSafe(24)
	if errState != nil {
		return "", errState
	}
	prefix = strings.Trim(prefix, "-_ .")
	if prefix == "" {
		return value, nil
	}
	return prefix + "-" + value, nil
}

// randomURLSafe handles a random url safe.
func randomURLSafe(size int) (string, error) {
	if size <= 0 {
		size = 32
	}
	raw := make([]byte, size)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", errRead
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// validateOAuthState validates an o auth state.
func validateOAuthState(state string) error {
	// Resolve credential context before calling upstream OAuth services.
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", errInvalidOAuthState)
	}
	if len(trimmed) > maxOAuthStateLength {
		return fmt.Errorf("%w: too long", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: contains path separator", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: contains '..'", errInvalidOAuthState)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("%w: invalid character", errInvalidOAuthState)
		}
	}
	return nil
}

// deviceFlowAuthView picks the client-facing verification URL and user code.
// verification_uri_complete is preferred. A bare verification_uri requires user_code.
func deviceFlowAuthView(completeURI, verificationURI, userCode string) (authURL, code string, err error) {
	code = strings.TrimSpace(userCode)
	completeURI = strings.TrimSpace(completeURI)
	verificationURI = strings.TrimSpace(verificationURI)
	if completeURI != "" {
		return completeURI, code, nil
	}
	if verificationURI != "" {
		if code == "" {
			return "", "", fmt.Errorf("device flow missing user code")
		}
		return verificationURI, code, nil
	}
	return "", "", fmt.Errorf("device flow missing verification url")
}

// normalizeOAuthProvider normalizes an o auth provider.
func normalizeOAuthProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "anthropic", nil
	case "codex", "openai":
		return "codex", nil
	case "antigravity", "anti-gravity":
		return "antigravity", nil
	case "kimi":
		return "kimi", nil
	case "kimi-ai":
		return "kimi-ai", nil
	case "xai", "x-ai", "x.ai", "grok":
		return "xai", nil
	case "devin":
		return "devin", nil
	case "meta", "muse":
		return "meta", nil
	default:
		return "", errUnsupportedProvider
	}
}

// authStatusMessage handles an auth status message.
func authStatusMessage(provider string, err error) string {
	if err == nil {
		return ""
	}
	switch provider {
	case "anthropic", "codex", "xai", "devin":
		return "Failed to exchange authorization code for tokens"
	case "antigravity":
		return "Failed to exchange token"
	default:
		return "Authentication failed"
	}
}

// oauthConfig handles an oauth config.
func (h *Handler) oauthConfig() *config.Config {
	if h != nil && h.runtime != nil {
		if cfg := h.runtime.Config(); cfg != nil {
			return cfg
		}
	}
	return &config.Config{}
}

// oauthHTTPClient handles an oauth http client.
func (h *Handler) oauthHTTPClient() *http.Client {
	cfg := h.oauthConfig()
	sdkCfg := cfg.SDKConfig
	if strings.TrimSpace(sdkCfg.ProxyURL) == "" {
		sdkCfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	return util.SetProxy(&sdkCfg, &http.Client{Timeout: 60 * time.Second})
}

// requestContextOrBackground handles a request context or background.
func requestContextOrBackground(c *gin.Context) context.Context {
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		return c.Request.Context()
	}
	return context.Background()
}

// antigravityCredentialFileName handles an antigravity credential file name.
func antigravityCredentialFileName(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return "antigravity.json"
	}
	return fmt.Sprintf("antigravity-%s.json", email)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// oauthSessionStateKey binds credential persistence to the session that authorized it.
type oauthSessionStateKey struct{}

// oauthSessionContext stops in-flight acquisition when another Home node cancels the session.
// The database transaction remains the authority for credential persistence.
func (h *Handler) oauthSessionContext(parent context.Context, state string, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithValue(parent, oauthSessionStateKey{}, state), timeout)
	session, errSession := h.repo.GetOAuthSession(ctx, state)
	if errSession != nil || session == nil || session.Status != "" {
		cancel()
		return ctx, cancel
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, errCurrent := h.repo.GetOAuthSession(ctx, state)
				if errCurrent != nil || current == nil || current.Status == "error" {
					cancel()
					return
				}
				if current.Status == "complete" {
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}
