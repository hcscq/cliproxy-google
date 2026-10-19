package management

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	log "github.com/sirupsen/logrus"
)

// Invalid-credential cleanup (panel 清理无效凭据): GET /credentials/forbidden groups
// credentials with 403 evidence by normalized reason, plus credentials Home already
// disabled for a terminal 401 (unauthorized / dead refresh token); POST
// /credentials/forbidden/purge backs each one up to kv_store (no TTL) and soft-deletes
// it. Transient one-off 403s are flagged non-persistent so the panel does not sweep
// them by default; temporary states (429, egress pause, cooling) never qualify.

const (
	forbiddenTOS        = "tos_violation"
	forbiddenVerify     = "verify_required"
	forbiddenPermission = "permission_denied"
	forbiddenCatalog    = "catalog_403"
	forbiddenOther      = "other_403"
	invalidUnauthorized = "unauthorized_401"
	invalidRefreshToken = "refresh_invalid"

	forbiddenBackupPrefix = "backup:purge403:"
	forbiddenAuditPrefix  = "audit:purge403:"
)

var forbiddenLabels = map[string]string{
	forbiddenTOS:        "封禁/违反条款",
	forbiddenVerify:     "需验证账号",
	forbiddenPermission: "无权限 (caller does not have permission)",
	forbiddenCatalog:    "模型列表 403",
	forbiddenOther:      "其他 403",
	invalidUnauthorized: "授权失效 (401)",
	invalidRefreshToken: "Refresh token 失效/已吊销",
}

type forbiddenCred struct {
	ID         string    `json:"id"`
	Seq        int64     `json:"seq,omitempty"`
	Label      string    `json:"label"`
	Category   string    `json:"category"`
	Kind       string    `json:"kind"` // "403" | "401"
	Message    string    `json:"message"`
	Source     string    `json:"source"`
	Last403At  time.Time `json:"last_403_at,omitzero"`
	Count7d    int64     `json:"count_403_7d"`
	OK7d       int64     `json:"ok_7d"`
	Disabled   bool      `json:"disabled"`
	Persistent bool      `json:"persistent"`
}

type forbiddenCategory struct {
	Key        string          `json:"key"`
	Label      string          `json:"label"`
	Count      int             `json:"count"`
	Persistent int             `json:"persistent"`
	Items      []forbiddenCred `json:"items"`
}

func classifyForbidden(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "terms of service"), strings.Contains(m, "tos_violation"),
		strings.Contains(m, "account_suspended"), strings.Contains(m, "has been suspended"),
		strings.Contains(m, "account_disabled"), strings.Contains(m, "user_disabled"),
		strings.Contains(m, "has been disabled"):
		return forbiddenTOS
	case strings.Contains(m, "validation_required"), strings.Contains(m, "verify your account"),
		strings.Contains(m, "account_verification_required"):
		return forbiddenVerify
	case strings.Contains(m, "caller does not have permission"), strings.Contains(m, "permission_denied"),
		strings.Contains(m, "permission denied"):
		return forbiddenPermission
	}
	return forbiddenOther
}

// forbiddenEvidence returns the strongest 403 signal for one credential, or ok=false.
func forbiddenEvidence(a *coreauth.Auth, cat *cluster.AntigravityCatalogRecord) (cred forbiddenCred, ok bool) {
	cred = forbiddenCred{ID: a.ID, Label: a.Label, Disabled: a.Disabled}
	pick := func(src, msg string, at time.Time) {
		if at.After(cred.Last403At) {
			cred.Last403At = at
		}
		if !ok || cred.Category == forbiddenOther {
			cred.Source, cred.Message, cred.Category = src, msg, classifyForbidden(msg)
		}
		ok = true
	}
	if a.Metadata != nil {
		if r, _ := a.Metadata["auto_disabled_reason"].(string); r != "" && a.Disabled {
			at, _ := time.Parse(time.RFC3339, stringFromAny(a.Metadata["auto_disabled_at"]))
			msg := r
			if a.LastError != nil && a.LastError.Message != "" {
				msg = r + ": " + a.LastError.Message
			}
			pick("auto_disable", msg, at)
		}
	}
	if a.LastError != nil && a.LastError.HTTPStatus == http.StatusForbidden {
		pick("last_error", strings.TrimSpace(a.LastError.Code+" "+a.LastError.Message), a.UpdatedAt)
	}
	for model, st := range a.ModelStates {
		if st != nil && st.LastError != nil && st.LastError.HTTPStatus == http.StatusForbidden {
			pick("model:"+model, strings.TrimSpace(st.LastError.Code+" "+st.LastError.Message), st.UpdatedAt)
		}
	}
	if cat != nil && cat.HTTPStatus == http.StatusForbidden {
		before := ok
		pick("catalog", cat.Error, cat.AttemptedAt)
		if !before {
			cred.Category = forbiddenCatalog
		}
	}
	if len(cred.Message) > 240 {
		cred.Message = cred.Message[:240]
	}
	return cred, ok
}

// unauthorizedEvidence reports a credential Home has already disabled for a terminal
// auth failure (401 / dead refresh token). Enabled credentials with a stray 401 are
// left alone: Home only disables after a non-transient failure.
func unauthorizedEvidence(a *coreauth.Auth) (cred forbiddenCred, ok bool) {
	if a == nil || !a.Disabled {
		return cred, false
	}
	le := a.LastError
	if !strings.EqualFold(strings.TrimSpace(a.StatusMessage), "unauthorized") &&
		(le == nil || (le.HTTPStatus != http.StatusUnauthorized && !strings.EqualFold(strings.TrimSpace(le.Code), "unauthorized"))) {
		return cred, false
	}
	cred = forbiddenCred{ID: a.ID, Label: a.Label, Disabled: true, Kind: "401", Source: "status", Category: invalidUnauthorized, Last403At: a.UpdatedAt, Persistent: true}
	msg := "unauthorized"
	if le != nil {
		msg = strings.TrimSpace(le.Code + " " + le.Message)
	}
	if isDeadRefreshTokenMessage(msg) {
		cred.Category = invalidRefreshToken
	}
	if len(msg) > 240 {
		msg = msg[:240]
	}
	cred.Message = msg
	return cred, true
}

func isDeadRefreshTokenMessage(msg string) bool {
	m := strings.ToLower(msg)
	for _, k := range []string{"invalid_grant", "refresh_token_expired", "refresh_token_revoked", "refresh_token_reused", "token has been expired or revoked", "token_revoked"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

func (h *Handler) collectForbidden(ctx context.Context) ([]forbiddenCred, map[string]*coreauth.Auth, error) {
	auths, err := h.repo.ListAuths(ctx)
	if err != nil {
		return nil, nil, err
	}
	seqs, _ := h.repo.AuthSeqs(ctx)
	cats, _ := h.repo.ListAntigravityCatalogs(ctx)
	stats := h.credentialCallStats(ctx, "7d", 7*24*time.Hour)
	byID := map[string]*coreauth.Auth{}
	var out []forbiddenCred
	for _, a := range auths {
		if !isOAuthAuth(a) {
			continue
		}
		var cat *cluster.AntigravityCatalogRecord
		if r, ok := cats[a.ID]; ok {
			cat = &r
		}
		cred, ok := forbiddenEvidence(a, cat)
		if ok {
			cred.Kind = "403"
		} else if cred, ok = unauthorizedEvidence(a); !ok {
			continue
		}
		cred.Seq = seqs[a.ID]
		if cred.Kind == "401" {
			if s := stats[a.ID]; s != nil {
				cred.OK7d = s.OK
			}
			byID[a.ID] = a
			out = append(out, cred)
			continue
		}
		if s := stats[a.ID]; s != nil {
			cred.Count7d, cred.OK7d = s.ByClass["403"], s.OK
		}
		// Persistent: auto-disabled for an account-level 403, or the 403 is the current
		// state with repeated occurrences and no later successes worth trusting.
		cred.Persistent = cred.Source == "auto_disable" ||
			(cred.Count7d >= 3 && cred.OK7d == 0) ||
			(cred.Source == "catalog" && cred.Disabled)
		byID[a.ID] = a
		out = append(out, cred)
	}
	return out, byID, nil
}

// ListForbiddenCredentials: GET /credentials/forbidden
func (h *Handler) ListForbiddenCredentials(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	creds, _, err := h.collectForbidden(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", err)
		return
	}
	groups := map[string]*forbiddenCategory{}
	for _, cr := range creds {
		g := groups[cr.Category]
		if g == nil {
			g = &forbiddenCategory{Key: cr.Category, Label: forbiddenLabels[cr.Category]}
			groups[cr.Category] = g
		}
		g.Items = append(g.Items, cr)
		g.Count++
		if cr.Persistent {
			g.Persistent++
		}
	}
	order := []string{forbiddenTOS, forbiddenVerify, forbiddenPermission, forbiddenCatalog, forbiddenOther, invalidUnauthorized, invalidRefreshToken}
	out := []forbiddenCategory{}
	for _, k := range order {
		if g := groups[k]; g != nil {
			sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].Seq < g.Items[j].Seq })
			out = append(out, *g)
		}
	}
	c.JSON(http.StatusOK, gin.H{"categories": out, "total": len(creds)})
}

// PurgeForbiddenCredentials: POST /credentials/forbidden/purge {ids, dry_run}
func (h *Handler) PurgeForbiddenCredentials(c *gin.Context) {
	var req struct {
		IDs    []string `json:"ids"`
		DryRun bool     `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "ids is required", err)
		return
	}
	if len(req.IDs) > 5000 {
		respondError(c, http.StatusBadRequest, "too many ids (max 5000)", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	creds, byID, err := h.collectForbidden(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", err)
		return
	}
	eligible := map[string]forbiddenCred{}
	for _, cr := range creds {
		eligible[cr.ID] = cr
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var deleted, skipped []string
	var failed []string
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		cr, ok := eligible[id]
		if !ok {
			skipped = append(skipped, id) // no 403 / terminal-401 evidence any more
			continue
		}
		if req.DryRun {
			deleted = append(deleted, id)
			continue
		}
		raw, _ := json.Marshal(gin.H{"auth": byID[id], "evidence": cr, "purged_at": time.Now().UTC()})
		if _, errSet := h.repo.KVSet(ctx, forbiddenBackupPrefix+stamp+":"+id, raw, 0, cluster.KVSetModeAlways); errSet != nil {
			failed = append(failed, id)
			continue
		}
		if errDel := h.repo.SoftDeleteAuth(ctx, id); errDel != nil {
			failed = append(failed, id)
			continue
		}
		deleted = append(deleted, id)
	}
	if !req.DryRun && len(deleted) > 0 {
		audit, _ := json.Marshal(gin.H{"at": time.Now().UTC(), "client_ip": c.ClientIP(), "deleted": deleted, "skipped": skipped, "failed": failed, "backup_prefix": forbiddenBackupPrefix + stamp + ":"})
		_, _ = h.repo.KVSet(ctx, forbiddenAuditPrefix+stamp, audit, 0, cluster.KVSetModeAlways)
		log.WithFields(log.Fields{"deleted": len(deleted), "skipped": len(skipped), "failed": len(failed), "client_ip": c.ClientIP(), "backup": forbiddenBackupPrefix + stamp}).Warn("purged invalid credentials")
		_ = h.refreshAuths(ctx)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "dry_run": req.DryRun, "deleted": deleted, "skipped": skipped, "failed": failed, "backup_prefix": forbiddenBackupPrefix + stamp + ":"})
}
