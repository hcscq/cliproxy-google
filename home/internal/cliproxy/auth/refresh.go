package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPIHome/internal/auth/oautherror"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/logging"
	log "github.com/sirupsen/logrus"
)

// refreshCredential refreshes auth metadata when a refresh token or Meta DCA token is present.
// It is best-effort: providers that do not support refresh are treated as no-op.
func refreshCredential(ctx context.Context, cfg *config.Config, auth *Auth, rt http.RoundTripper) (*Auth, error) {
	// Resolve credential context before calling upstream OAuth services.
	if auth == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	switch provider {
	case "antigravity":
		return refreshAntigravity(ctx, cfg, auth, rt)
	default:
		return auth, nil
	}
}

// providerRefreshError carries diagnostics returned by a built-in provider.
type providerRefreshError struct {
	diagnostic string
	cause      error
}

func (e *providerRefreshError) Error() string {
	if e == nil {
		return ""
	}
	type upstreamResponseError interface {
		ResponseBody() []byte
	}
	var upstream upstreamResponseError
	if errors.As(e.cause, &upstream) && upstream != nil {
		return string(upstream.ResponseBody())
	}
	return e.diagnostic
}

func (e *providerRefreshError) StatusCode() int {
	return http.StatusServiceUnavailable
}

func (e *providerRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *providerRefreshError) Diagnostic() string {
	if e == nil {
		return ""
	}
	return e.diagnostic
}

// builtInRefreshError preserves diagnostics emitted by built-in provider
// clients, including transport and response-read failures.
func builtInRefreshError(provider, stage string, errRefresh error) error {
	if errRefresh == nil {
		return nil
	}
	if errors.Is(errRefresh, context.Canceled) {
		return errRefresh
	}
	var existing *providerRefreshError
	if errors.As(errRefresh, &existing) && existing != nil {
		return errRefresh
	}
	diagnostic := logging.SafeErrorDiagnostic(errRefresh)
	type diagnosticError interface {
		Diagnostic() string
	}
	var upstream diagnosticError
	if errors.As(errRefresh, &upstream) && upstream != nil {
		if upstreamDiagnostic := strings.TrimSpace(upstream.Diagnostic()); upstreamDiagnostic != "" {
			diagnostic = upstreamDiagnostic
		}
	}
	provider = strings.TrimSpace(provider)
	stage = strings.TrimSpace(stage)
	prefix := provider + " refresh failed"
	if provider == "" {
		prefix = "credential refresh failed"
	}
	if stage != "" {
		prefix += ": stage=" + stage
	}
	return &providerRefreshError{
		diagnostic: fmt.Sprintf("%s err=%s", prefix, diagnostic),
		cause:      errRefresh,
	}
}

// antigravityOAuthRefreshError classifies terminal OAuth failures while
// preserving the upstream response diagnostics.
func antigravityOAuthRefreshError(statusCode int, body []byte) error {
	upstream := oautherror.NewResponseError(statusCode, body)
	code := strings.TrimSpace(upstream.OAuthError())
	if code == "" {
		code = strings.TrimSpace(upstream.Code())
	}
	cause := error(upstream)
	if strings.EqualFold(code, "invalid_grant") || isTerminalOAuthRefreshDescription(strings.Join([]string{upstream.ErrorDescription(), upstream.Message(), upstream.Detail()}, " ")) {
		cause = fmt.Errorf("%w: %w", newUnauthorizedRefreshError(), upstream)
	}
	return builtInRefreshError("antigravity", "upstream_response", cause)
}

// isTerminalOAuthRefreshDescription recognizes explicit expired or revoked
// refresh-token descriptions returned by OAuth providers.
func isTerminalOAuthRefreshDescription(description string) bool {
	normalized := strings.ToLower(strings.TrimSpace(description))
	normalized = strings.NewReplacer("_", " ", "-", " ").Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized == "" {
		return false
	}
	for _, signal := range []string{
		"token has been expired or revoked",
		"refresh token has expired",
		"refresh token is expired",
		"refresh token expired",
		"refresh token has been revoked",
		"refresh token is revoked",
		"refresh token revoked",
		"refresh token has been reused",
		"refresh token is reused",
		"refresh token reused",
	} {
		if strings.Contains(normalized, signal) {
			return true
		}
	}
	return false
}

// refreshAntigravity refreshes an antigravity.
func refreshAntigravity(ctx context.Context, cfg *config.Config, auth *Auth, rt http.RoundTripper) (*Auth, error) {
	// Resolve credential context before calling upstream OAuth services.
	_ = cfg
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}

	form := url.Values{}
	form.Set("client_id", antigravity.ClientID)
	form.Set("client_secret", antigravity.ClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return nil, builtInRefreshError("antigravity", "request_build", errReq)
	}
	req.Header.Set("Host", "oauth2.googleapis.com")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Go-http-client/2.0")

	client := &http.Client{Timeout: 30 * time.Second}
	if rt != nil {
		client.Transport = rt
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, builtInRefreshError("antigravity", "transport", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity refresh: response body close error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, builtInRefreshError("antigravity", "response_read", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, antigravityOAuthRefreshError(resp.StatusCode, body)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(body, &tokenResp); errUnmarshal != nil {
		return nil, builtInRefreshError("antigravity", "response_decode", errUnmarshal)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	now := time.Now()
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	auth.Metadata["timestamp"] = now.UnixMilli()
	auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = "antigravity"
	auth.Metadata["last_refresh"] = now.Format(time.RFC3339)
	return auth, nil
}

func metaStringValue(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
