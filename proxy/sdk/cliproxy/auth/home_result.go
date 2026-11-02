package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const homeResultExecutorType = "home-result"

// ReportHomeUnauthorized publishes a result-only zero-token usage record for an
// upstream 401 attempt that did not pass through an executor UsageReporter.
func (m *Manager) ReportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model string, upstreamBody ...[]byte) {
	failureBody := ""
	if len(upstreamBody) > 0 {
		failureBody = string(upstreamBody[0])
	}
	m.reportHomeUnauthorized(ctx, auth, provider, model, AccessTokenSHA256(auth), failureBody)
}

func (m *Manager) reportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model, accessTokenSHA256, failureBody string) {
	if m == nil || auth == nil {
		return
	}
	authIndex := strings.TrimSpace(auth.Index)
	if authIndex == "" {
		authIndex = strings.TrimSpace(auth.EnsureIndex())
	}
	accessTokenSHA256 = strings.TrimSpace(accessTokenSHA256)
	if authIndex == "" || accessTokenSHA256 == "" {
		return
	}
	if failureBody == "" {
		failureBody = "upstream unauthorized"
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = strings.TrimSpace(auth.Provider)
	}
	model = strings.TrimSpace(model)
	alias := strings.TrimSpace(coreusage.RequestedModelAliasFromContext(ctx))
	if alias == "" {
		alias = model
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:          provider,
		ExecutorType:      homeResultExecutorType,
		Model:             model,
		Alias:             alias,
		SessionID:         clientMeta.SessionID,
		ParentSessionID:   clientMeta.ParentSessionID,
		AuthID:            auth.ID,
		AuthIndex:         authIndex,
		AccessTokenSHA256: accessTokenSHA256,
		AuthType:          auth.AuthKind(),
		Source:            auth.AuthSourceKind(),
		ReasoningEffort:   coreusage.ReasoningEffortFromContext(ctx),
		ServiceTier:       coreusage.ServiceTierFromContext(ctx),
		Generate:          coreusage.GenerateFlag(false),
		RequestedAt:       time.Now(),
		Failed:            true,
		Fail: coreusage.Failure{
			StatusCode: http.StatusUnauthorized,
			Body:       failureBody,
		},
	})
}

func noteHomeUnavailable(_ homeAuthDispatcher) {
	if c := home.Current(); c != nil {
		c.NoteHomeUnavailable()
	}
}

func noteHomeAvailable(_ homeAuthDispatcher) {
	if c := home.Current(); c != nil {
		c.NoteHomeAvailable()
	}
}

const homeDispatchUnavailableExecutorType = "home-dispatch"

// reportHomeDispatchUnavailable records a pre-auth home_unavailable 503 in usage.
func reportHomeDispatchUnavailable(ctx context.Context, message string) {
	if ctx == nil {
		ctx = context.Background()
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "home_unavailable"
	}
	if !strings.Contains(strings.ToLower(message), "home_unavailable") {
		message = "home_unavailable: " + message
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	alias := strings.TrimSpace(coreusage.RequestedModelAliasFromContext(ctx))
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:        "home",
		ExecutorType:    homeDispatchUnavailableExecutorType,
		Model:           "home_unavailable",
		Alias:           alias,
		SessionID:       clientMeta.SessionID,
		ParentSessionID: clientMeta.ParentSessionID,
		RequestedAt:     time.Now(),
		Failed:          true,
		Fail: coreusage.Failure{
			StatusCode: http.StatusServiceUnavailable,
			Body:       message,
		},
	})
}
