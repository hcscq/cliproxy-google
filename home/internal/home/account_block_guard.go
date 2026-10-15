package home

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Account block guard: some Antigravity 403s are account-level, e.g.
// 403 PERMISSION_DENIED reason VALIDATION_REQUIRED "Verify your account to continue."
// They hit every model, but the generic 403 handling only cools the one model for 30
// minutes, so the credential kept burning one request per model per 30 minutes. The Home
// Homes scan their in-memory auth state (no DB reads per request) and, on a clear
// account-level 403, disables the credential with a visible reason. It is re-enabled by the
// normal 启用 toggle, or automatically when a low-frequency recheck probe returns 200.

const (
	// AccountBlockReasonKey is the metadata key holding the auto-disable reason code.
	AccountBlockReasonKey = "auto_disabled_reason"
	accountBlockAtKey     = "auto_disabled_at"
	// AccountBlockClearedKey marks when the block was last lifted; older 403 states are ignored.
	AccountBlockClearedKey = "auto_disabled_cleared_at"

	accountBlockVerify    = "account_verification_required"
	accountBlockSuspended = "account_suspended"

	accountBlockScanEvery    = 10 * time.Second
	accountBlockRecheckKV    = "account-block-recheck:"
	accountBlockProbeTimeout = 30 * time.Second
)

// classifyAccountBlock returns a reason code and label for account-level 403 bodies.
func classifyAccountBlock(status int, message string) (string, string) {
	if status != http.StatusForbidden {
		return "", ""
	}
	m := strings.ToLower(message)
	switch {
	case strings.Contains(m, "validation_required"), strings.Contains(m, "verify your account"):
		return accountBlockVerify, "需验证账号（403）"
	case strings.Contains(m, "account_disabled"), strings.Contains(m, "user_disabled"),
		strings.Contains(m, "has been suspended"), strings.Contains(m, "account suspended"),
		strings.Contains(m, "terms of service"), strings.Contains(m, "tos_violation"):
		return accountBlockSuspended, "账号已停用/违反条款（403）"
	}
	return "", ""
}

func upstreamMessage(body string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return strings.TrimSpace(parsed.Error.Message)
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > 160 {
		body = body[:160]
	}
	return body
}

func metadataTime(auth *coreauth.Auth, key string) time.Time {
	if auth == nil || auth.Metadata == nil {
		return time.Time{}
	}
	if s, ok := auth.Metadata[key].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

type accountBlockFinding struct {
	code, label, upstream, model string
	at                           time.Time
}

// findAccountBlock looks for a fresh account-level 403 in the credential's model states.
func findAccountBlock(auth *coreauth.Auth) (accountBlockFinding, bool) {
	if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
		return accountBlockFinding{}, false
	}
	cleared := metadataTime(auth, AccountBlockClearedKey)
	for model, st := range auth.ModelStates {
		if st == nil || st.LastError == nil || st.LastError.HTTPStatus != http.StatusForbidden {
			continue
		}
		if !cleared.IsZero() && !st.UpdatedAt.After(cleared) {
			continue
		}
		code, label := classifyAccountBlock(st.LastError.HTTPStatus, st.LastError.Message+" "+st.LastError.Code)
		if code == "" {
			continue
		}
		return accountBlockFinding{code: code, label: label, upstream: upstreamMessage(st.LastError.Message), model: model, at: st.UpdatedAt}, true
	}
	return accountBlockFinding{}, false
}

func (r *Runtime) accountBlockRecheckInterval() (time.Duration, bool) {
	if cfg := r.Config(); cfg != nil {
		return cfg.AccountBlockRecheckInterval()
	}
	return 6 * time.Hour, true
}

func (r *Runtime) startAccountBlockGuard(ctx context.Context) {
	go func() {
		tick := time.NewTicker(accountBlockScanEvery)
		defer tick.Stop()
		lastRecheck := map[string]time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if r.coreManager == nil {
				continue
			}
			// Every Home scans (usage results land on whichever Home ingests them);
			// the disable is idempotent. Only the leader runs rechecks.
			now := time.Now().UTC()
			interval, recheck := r.accountBlockRecheckInterval()
			recheck = recheck && r.catalogLeader()
			for _, a := range r.coreManager.ListMatching(func(a *coreauth.Auth) bool {
				_, blocked := findAccountBlock(a)
				return blocked || (recheck && a.Disabled && a.Metadata != nil && a.Metadata[AccountBlockReasonKey] != nil)
			}) {
				if f, ok := findAccountBlock(a); ok {
					r.disableForAccountBlock(ctx, a.ID, f, now)
					continue
				}
				if recheck && a.Disabled && a.Metadata != nil && a.Metadata[AccountBlockReasonKey] != nil {
					since := metadataTime(a, accountBlockAtKey)
					if last := lastRecheck[a.ID]; last.After(since) {
						since = last
					}
					if now.Sub(since) >= interval {
						lastRecheck[a.ID] = now
						r.recheckAccountBlock(ctx, a, now)
					}
				}
			}
		}
	}()
}

func (r *Runtime) fullAuth(ctx context.Context, id string) *coreauth.Auth {
	if r.clusterAdapter != nil && r.clusterAdapter.Enabled() {
		full, err := r.clusterAdapter.GetFullAuth(ctx, id)
		if err != nil || full == nil {
			return nil
		}
		return full
	}
	a, _ := r.coreManager.GetByID(id)
	return a
}

func (r *Runtime) disableForAccountBlock(ctx context.Context, id string, f accountBlockFinding, now time.Time) {
	full := r.fullAuth(ctx, id)
	if full == nil || full.Disabled {
		return
	}
	if full.Metadata == nil {
		full.Metadata = map[string]any{}
	}
	full.Disabled = true
	full.Status = coreauth.StatusDisabled
	full.StatusMessage = f.label + " · " + now.In(quotaBucketDisplayZone).Format("01-02 15:04") + " 停用：" + f.upstream
	full.LastError = &coreauth.Error{Code: f.code, HTTPStatus: http.StatusForbidden, Message: f.upstream}
	full.Metadata["disabled"] = true
	full.Metadata[AccountBlockReasonKey] = f.code
	full.Metadata[accountBlockAtKey] = now.Format(time.RFC3339)
	full.UpdatedAt = now
	if _, err := r.coreManager.Update(ctx, full); err != nil {
		log.WithError(err).Warnf("account block guard: disable %s failed", id)
		return
	}
	log.Warnf("account block guard: %s (%s) disabled: %s (model %s): %s", id, full.Label, f.code, f.model, f.upstream)
}

// recheckAccountBlock sends one tiny request; 200 re-enables the credential.
func (r *Runtime) recheckAccountBlock(ctx context.Context, a *coreauth.Auth, now time.Time) {
	pctx, cancel := context.WithTimeout(ctx, accountBlockProbeTimeout)
	defer cancel()
	full := r.fullAuth(pctx, a.ID)
	if full == nil {
		return
	}
	model := "gemini-2.5-flash"
	for _, info := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
		if info != nil && antigravityModelBucket(info.ID) == quotaBucketGemini {
			model = info.ID
			break
		}
	}
	code, _, body, err := sendQuotaProbe(pctx, full, model)
	result := "error"
	switch {
	case err != nil:
		result = "error: " + err.Error()
	case code >= 200 && code < 300 || code == http.StatusTooManyRequests:
		// 429 means the account itself is accepted again (only quota is short).
		result = "ok"
		r.liftAccountBlock(pctx, full, now)
	default:
		result = http.StatusText(code) + ": " + upstreamMessage(string(body))
	}
	if raw, errJSON := json.Marshal(map[string]any{"at": now, "result": result, "http_status": code}); errJSON == nil {
		_, _ = r.KVSet(pctx, accountBlockRecheckKV+a.ID, raw, 48*time.Hour, "")
	}
	log.Infof("account block guard: recheck %s (%s) -> %d %s", a.ID, a.Label, code, result)
}

func (r *Runtime) liftAccountBlock(ctx context.Context, full *coreauth.Auth, now time.Time) {
	if full.Metadata == nil {
		full.Metadata = map[string]any{}
	}
	full.Disabled = false
	full.Status = coreauth.StatusActive
	full.StatusMessage = ""
	full.LastError = nil
	full.Metadata["disabled"] = false
	delete(full.Metadata, AccountBlockReasonKey)
	delete(full.Metadata, accountBlockAtKey)
	full.Metadata[AccountBlockClearedKey] = now.Format(time.RFC3339)
	full.UpdatedAt = now
	if _, err := r.coreManager.Update(ctx, full); err != nil {
		log.WithError(err).Warnf("account block guard: re-enable %s failed", full.ID)
		return
	}
	log.Infof("account block guard: %s (%s) re-enabled after recheck", full.ID, full.Label)
	r.RequestSubscriptionCheck(full.ID, "re-enabled")
}
