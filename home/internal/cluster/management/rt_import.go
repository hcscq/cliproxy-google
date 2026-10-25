package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/auth/antigravity"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/proxyutil"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Refresh-token import for Antigravity credentials.
//
// Every antigravity entry that arrives without a complete identity (access
// token, email, project) is enriched at import time: the refresh token is
// exchanged for an access token (dead tokens are rejected with a clear reason),
// the email comes from userinfo and the project from loadCodeAssist/onboardUser.
// Entries are then deduplicated against existing credentials by email
// (case-insensitive) and refresh token; a match updates the existing
// credential's tokens/project and keeps its groups, notes and settings.
// Tokens are never logged or echoed back; results carry only a short hint.

const (
	rtImportMaxItemsPerRequest = 100
	rtImportDefaultConcurrency = 6
	rtImportMaxConcurrency     = 16
	rtImportItemTimeout        = 60 * time.Second
)

// antigravityEnrichment is what import-time enrichment learns about one refresh token.
type antigravityEnrichment struct {
	AccessToken  string
	RefreshToken string // rotated token when Google returns one
	ExpiresIn    int64
	Email        string
	ProjectID    string
	ProjectError string // project resolution failed (credential still usable; proxy retries)
	// EmailMismatch is the supplied email when Google reports a different account.
	EmailMismatch string
	// EmailUnverified means userinfo failed and the supplied email was used as-is.
	EmailUnverified bool
	// Egress* is the sticky egress the enrichment calls used; a new credential keeps it.
	EgressProxyURL, EgressName, EgressIP string
	EgressExplicit                       bool
}

// antigravityEnricher resolves an antigravity identity from a refresh token.
// knownEmail/knownProject skip the corresponding lookups.
type antigravityEnricher interface {
	Enrich(ctx context.Context, refreshToken, knownEmail, knownProject string) (*antigravityEnrichment, error)
}

// rtImportError is a user-facing, token-free failure reason.
type rtImportError struct {
	Code   string
	Reason string
}

func (e *rtImportError) Error() string { return e.Code + ": " + e.Reason }

// SetAntigravityEnricher overrides the upstream enricher (tests).
func (h *Handler) SetAntigravityEnricher(enricher antigravityEnricher) {
	if h != nil {
		h.agEnricher = enricher
	}
}

func (h *Handler) antigravityEnricher() antigravityEnricher {
	if h.agEnricher != nil {
		return h.agEnricher
	}
	return &googleAntigravityEnricher{h: h}
}

type googleAntigravityEnricher struct{ h *Handler }

func (g *googleAntigravityEnricher) Enrich(ctx context.Context, refreshToken, knownEmail, knownProject string) (*antigravityEnrichment, error) {
	out := &antigravityEnrichment{}
	form := url.Values{}
	form.Set("client_id", antigravity.ClientID)
	form.Set("client_secret", antigravity.ClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, antigravityTokenEndpoint, strings.NewReader(form.Encode()))
	if errReq != nil {
		return nil, &rtImportError{Code: "internal", Reason: "could not build token request"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, errDo := g.h.oauthHTTPClientFor(ctx).Do(req)
	if errDo != nil {
		return nil, &rtImportError{Code: "network", Reason: "token endpoint unreachable: " + redactTransportError(errDo)}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyTokenRefreshFailure(resp.StatusCode, body)
	}
	out.AccessToken = strings.TrimSpace(gjson.GetBytes(body, "access_token").String())
	out.RefreshToken = strings.TrimSpace(gjson.GetBytes(body, "refresh_token").String())
	out.ExpiresIn = gjson.GetBytes(body, "expires_in").Int()
	if out.AccessToken == "" {
		return nil, &rtImportError{Code: "token_invalid", Reason: "token endpoint returned no access token"}
	}
	// Always ask Google who the token belongs to; a supplied email is only a fallback
	// (and is checked so an email----token mismatch cannot merge into the wrong account).
	known := strings.TrimSpace(knownEmail)
	email, errEmail := g.h.fetchAntigravityEmail(ctx, out.AccessToken)
	switch {
	case errEmail == nil:
		out.Email = email
		if known != "" && !strings.EqualFold(known, email) {
			out.EmailMismatch = known
		}
	case known != "":
		out.Email = known
		out.EmailUnverified = true
	default:
		return nil, &rtImportError{Code: "userinfo_failed", Reason: "could not read account email (token may lack the userinfo.email scope): " + shortReason(errEmail.Error())}
	}
	out.ProjectID = strings.TrimSpace(knownProject)
	if out.ProjectID == "" {
		projectID, errProject := g.h.fetchAntigravityProjectID(ctx, out.AccessToken)
		if errProject != nil || strings.TrimSpace(projectID) == "" {
			reason := "empty project id"
			if errProject != nil {
				reason = shortReason(errProject.Error())
			}
			out.ProjectError = reason
		} else {
			out.ProjectID = strings.TrimSpace(projectID)
		}
	}
	return out, nil
}

// classifyTokenRefreshFailure maps Google's token-endpoint errors to clear reasons.
func classifyTokenRefreshFailure(status int, body []byte) error {
	code := strings.TrimSpace(gjson.GetBytes(body, "error").String())
	desc := strings.TrimSpace(gjson.GetBytes(body, "error_description").String())
	switch code {
	case "invalid_grant":
		reason := "refresh token is invalid, expired or revoked (invalid_grant)"
		if desc != "" {
			reason += ": " + shortReason(desc)
		}
		return &rtImportError{Code: "token_dead", Reason: reason}
	case "unauthorized_client", "invalid_client":
		return &rtImportError{Code: "wrong_client", Reason: "token was not issued for the Antigravity OAuth client (" + code + ")"}
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return &rtImportError{Code: "upstream_unavailable", Reason: fmt.Sprintf("Google token endpoint returned %d; retry later", status)}
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", status)
	}
	return &rtImportError{Code: "token_rejected", Reason: "token refresh rejected: " + code + optionalSuffix(shortReason(desc))}
}

func optionalSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func shortReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

func redactTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return shortReason(urlErr.Err.Error())
	}
	return shortReason(err.Error())
}

// tokenHint is a non-secret identifier for a token in results ("…abcd").
func tokenHint(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		return "…"
	}
	return "…" + token[len(token)-4:]
}

// ---- input parsing ---------------------------------------------------------

// rtImportEntry is one token (plus optional known fields) to import.
type rtImportEntry struct {
	Index        int
	RefreshToken string
	Email        string
	ProjectID    string
	Extra        map[string]any // other fields from a JSON object (label, note, prefix, …)
	Source       string         // file name for uploads
	ParseError   string
}

var (
	rtInvisibleReplacer = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "", "\u00a0", " ", "\u3000", " ", "\r\n", "\n", "\r", "\n")
	rtDashClass         = `[-\x{2010}-\x{2015}\x{2212}\x{FE58}\x{FE63}\x{FF0D}]`
	rtSepPattern        = regexp.MustCompile(`\s*` + rtDashClass + `{3,}\s*`)
	rtEmailSepPattern   = regexp.MustCompile(`^([^\s@]+@[^\s]+?)\s*` + rtDashClass + `{3,}\s*(\S.*)$`)
)

// normalizeRTText strips BOM/zero-width chars and normalizes line endings from copy-paste.
func normalizeRTText(text string) string { return rtInvisibleReplacer.Replace(text) }

// parseRefreshTokenInput accepts: one token per line; a JSON array of strings or
// objects; a single JSON object; or one JSON object per line.
func parseRefreshTokenInput(text string) []rtImportEntry {
	text = normalizeRTText(text)
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		var anyValue any
		if errJSON := json.Unmarshal([]byte(text), &anyValue); errJSON == nil {
			var values []any
			switch v := anyValue.(type) {
			case []any:
				values = v
			default:
				values = []any{v}
			}
			out := make([]rtImportEntry, 0, len(values))
			for i, value := range values {
				out = append(out, entryFromJSONValue(i, value))
			}
			return out
		}
	}
	lines := strings.Split(normalizeRTText(text), "\n")
	out := make([]rtImportEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := len(out)
		if strings.HasPrefix(line, "{") {
			var value any
			if errJSON := json.Unmarshal([]byte(line), &value); errJSON != nil {
				out = append(out, rtImportEntry{Index: idx, ParseError: "line is not valid JSON"})
				continue
			}
			out = append(out, entryFromJSONValue(idx, value))
			continue
		}
		email := ""
		if m := rtEmailSepPattern.FindStringSubmatch(line); m != nil {
			// "email----refresh_token" (3+ dashes, any dash variant; extra fields after the token are ignored)
			email = strings.TrimSpace(m[1])
			line = strings.TrimSpace(m[2])
			if loc := rtSepPattern.FindStringIndex(line); loc != nil {
				line = strings.TrimSpace(line[:loc[0]])
			}
		} else if strings.Contains(line, "----") {
			out = append(out, rtImportEntry{Index: idx, ParseError: "expected email----refresh_token"})
			continue
		}
		token := strings.Trim(line, `"', `)
		if strings.ContainsAny(token, " \t") {
			out = append(out, rtImportEntry{Index: idx, ParseError: "line contains spaces; expected one refresh token per line"})
			continue
		}
		out = append(out, rtImportEntry{Index: idx, RefreshToken: token, Email: email})
	}
	return out
}

func entryFromJSONValue(index int, value any) rtImportEntry {
	switch v := value.(type) {
	case string:
		return rtImportEntry{Index: index, RefreshToken: strings.TrimSpace(v)}
	case map[string]any:
		entry := rtImportEntry{Index: index, Extra: map[string]any{}}
		for key, raw := range v {
			switch key {
			case "refresh_token", "refreshToken":
				entry.RefreshToken = strings.TrimSpace(stringFromAny(raw))
			case "email":
				entry.Email = strings.TrimSpace(stringFromAny(raw))
			case "project_id", "projectId":
				entry.ProjectID = strings.TrimSpace(stringFromAny(raw))
			case "type", "access_token", "expires_in", "timestamp", "expired", "token":
				// recomputed by enrichment ("token" objects from other tools are ignored)
			default:
				entry.Extra[key] = raw
			}
		}
		if t := strings.ToLower(strings.TrimSpace(stringFromAny(v["type"]))); t != "" && t != "antigravity" {
			entry.ParseError = "object type is " + t + ", only antigravity is supported here"
		}
		if entry.RefreshToken == "" && entry.ParseError == "" {
			entry.ParseError = "object has no refresh_token"
		}
		return entry
	}
	return rtImportEntry{Index: index, ParseError: "unsupported JSON value"}
}

// ---- import pipeline -------------------------------------------------------

// RTImportResult is the token-free outcome for one entry.
type RTImportResult struct {
	Index        int    `json:"index"`
	Source       string `json:"source,omitempty"`
	TokenHint    string `json:"token_hint,omitempty"`
	Status       string `json:"status"` // created | updated | would_create | would_update | failed | skipped
	Code         string `json:"code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Warning      string `json:"warning,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
	Email        string `json:"email,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	MatchedBy    string `json:"matched_by,omitempty"` // email | refresh_token
}

// RTImportSummary aggregates results.
type RTImportSummary struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type rtImportOptions struct {
	DryRun      bool
	Concurrency int
	// Egress is "" / "auto" (least-loaded healthy, balanced within the batch) or an
	// egress name/IP; an explicit choice also re-pins matched existing credentials.
	Egress string
	// AllowPrimary lets auto-balance use node primary IPs (nil = global default).
	AllowPrimary *bool
	// Reserve puts newly created credentials into the credential reserve right away.
	Reserve bool
}

// credentialIndex finds existing antigravity credentials by email / refresh token.
type credentialIndex struct {
	byEmail map[string]*coreauth.Auth
	byToken map[string]*coreauth.Auth
}

func (h *Handler) buildCredentialIndex(ctx context.Context) (*credentialIndex, error) {
	auths, errList := h.repo.ListAuths(ctx)
	if errList != nil {
		return nil, errList
	}
	idx := &credentialIndex{byEmail: map[string]*coreauth.Auth{}, byToken: map[string]*coreauth.Auth{}}
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		idx.add(auth)
	}
	return idx, nil
}

func (idx *credentialIndex) add(auth *coreauth.Auth) {
	email := strings.ToLower(strings.TrimSpace(stringFromAny(auth.Metadata["email"])))
	if email != "" {
		if prev, ok := idx.byEmail[email]; !ok || (!auth.CreatedAt.IsZero() && auth.CreatedAt.Before(prev.CreatedAt)) {
			idx.byEmail[email] = auth
		}
	}
	if token := strings.TrimSpace(stringFromAny(auth.Metadata["refresh_token"])); token != "" {
		if _, ok := idx.byToken[token]; !ok {
			idx.byToken[token] = auth
		}
	}
}

func (idx *credentialIndex) match(email, token string) (*coreauth.Auth, string) {
	if token != "" {
		if auth := idx.byToken[token]; auth != nil {
			return auth, "refresh_token"
		}
	}
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		if auth := idx.byEmail[email]; auth != nil {
			return auth, "email"
		}
	}
	return nil, ""
}

// importAntigravityEntries enriches, dedupes and (unless dry-run) persists entries.
// It reloads the runtime once and triggers one quota probe round at the end.
func (h *Handler) importAntigravityEntries(ctx context.Context, entries []rtImportEntry, opts rtImportOptions) ([]RTImportResult, RTImportSummary, error) {
	results := make([]RTImportResult, len(entries))
	idx, errIndex := h.buildCredentialIndex(ctx)
	if errIndex != nil {
		return nil, RTImportSummary{}, errIndex
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = rtImportDefaultConcurrency
	}
	if concurrency > rtImportMaxConcurrency {
		concurrency = rtImportMaxConcurrency
	}
	seen := map[string]int{}
	type job struct{ i int }
	jobs := make(chan job)
	var wg sync.WaitGroup
	var writeMu sync.Mutex // serializes dedupe decisions + writes so duplicates in one batch merge
	touched := map[string]struct{}{}
	enricher := h.antigravityEnricher()
	picker, errPicker := h.newEgressPicker(ctx, opts.Egress, opts.AllowPrimary)
	if errPicker != nil {
		return nil, RTImportSummary{}, errPicker
	}

	for i := range entries {
		e := entries[i]
		results[i] = RTImportResult{Index: e.Index, Source: e.Source, TokenHint: tokenHint(e.RefreshToken)}
		if e.ParseError != "" {
			results[i].Status, results[i].Code, results[i].Reason = "failed", "invalid_input", e.ParseError
			continue
		}
		if e.RefreshToken == "" {
			results[i].Status, results[i].Code, results[i].Reason = "failed", "invalid_input", "empty refresh token"
			continue
		}
		if first, dup := seen[e.RefreshToken]; dup {
			results[i].Status, results[i].Code, results[i].Reason = "skipped", "duplicate_in_batch", fmt.Sprintf("same token as item %d", entries[first].Index+1)
			continue
		}
		seen[e.RefreshToken] = i
	}

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				e := entries[j.i]
				res := &results[j.i]
				itemCtx, cancel := context.WithTimeout(ctx, rtImportItemTimeout)
				egress, explicitEgress, errEgress := h.importEgress(idx, &writeMu, e, picker)
				if errEgress != nil {
					cancel()
					res.Status, res.Code, res.Reason = "failed", "no_egress", errEgress.Error()
					continue
				}
				if egress.ProxyURL != "" {
					itemCtx = withEgressProxy(itemCtx, egress.ProxyURL)
				}
				enriched, errEnrich := enricher.Enrich(itemCtx, e.RefreshToken, e.Email, e.ProjectID)
				cancel()
				if enriched != nil {
					enriched.EgressProxyURL, enriched.EgressName, enriched.EgressIP = egress.ProxyURL, egress.Name, egress.IP
					enriched.EgressExplicit = explicitEgress
				}
				if errEnrich != nil {
					res.Status = "failed"
					var ie *rtImportError
					if errors.As(errEnrich, &ie) {
						res.Code, res.Reason = ie.Code, ie.Reason
					} else {
						res.Code, res.Reason = "enrich_failed", shortReason(errEnrich.Error())
					}
					continue
				}
				res.Email, res.ProjectID = enriched.Email, enriched.ProjectID
				var warnings []string
				if enriched.EmailMismatch != "" {
					warnings = append(warnings, "supplied email "+enriched.EmailMismatch+" differs from the token's account; using "+enriched.Email)
				}
				if enriched.EmailUnverified {
					warnings = append(warnings, "email not verified (userinfo failed); using the supplied email")
				}
				if enriched.ProjectError != "" {
					warnings = append(warnings, "project_id not resolved ("+enriched.ProjectError+"); the proxy will retry on first request")
				}
				res.Warning = strings.Join(warnings, "; ")
				writeMu.Lock()
				existing, matchedBy := idx.match(enriched.Email, e.RefreshToken)
				if existing == nil && enriched.RefreshToken != "" {
					existing, matchedBy = idx.match("", enriched.RefreshToken)
				}
				res.MatchedBy = matchedBy
				switch {
				case opts.DryRun && existing != nil:
					res.Status, res.CredentialID = "would_update", existing.ID
				case opts.DryRun:
					res.Status = "would_create"
					// register a placeholder so later duplicates in the batch report would_update
					idx.add(&coreauth.Auth{ID: "", Provider: "antigravity", Metadata: map[string]any{"email": enriched.Email, "refresh_token": e.RefreshToken}})
				default:
					id, created, errStore := h.persistAntigravityImport(ctx, existing, e, enriched)
					if errStore != nil {
						res.Status, res.Code, res.Reason = "failed", "store_failed", shortReason(errStore.Error())
					} else {
						res.CredentialID = id
						if created && opts.Reserve && h.runtime != nil {
							if _, errRes := h.runtime.SetCredentialsReserved(ctx, []string{id}, true, "导入时设为储备"); errRes != nil {
								res.Warning = "设为储备失败：" + shortReason(errRes.Error())
							}
						}
						if created {
							res.Status = "created"
						} else {
							res.Status = "updated"
						}
						touched[id] = struct{}{}
						if stored, _, errGet := h.repo.GetAuth(ctx, id); errGet == nil && stored != nil {
							idx.add(stored)
						}
					}
				}
				writeMu.Unlock()
			}
		}()
	}
	for i := range entries {
		if results[i].Status == "" {
			jobs <- job{i: i}
		}
	}
	close(jobs)
	wg.Wait()

	summary := RTImportSummary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case "created", "would_create":
			summary.Created++
		case "updated", "would_update":
			summary.Updated++
		case "failed":
			summary.Failed++
		case "skipped":
			summary.Skipped++
		}
	}
	if !opts.DryRun && len(touched) > 0 {
		if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
			log.WithError(errRefresh).Warn("refresh-token import: runtime reload failed")
		}
		if h.quotaRecollect != nil {
			if _, errTrigger := h.quotaRecollect.TriggerCollection(context.WithoutCancel(ctx), touched, nil); errTrigger != nil {
				log.WithError(errTrigger).Warn("refresh-token import: quota probe trigger failed")
			}
		}
		log.Infof("refresh-token import: %d created, %d updated, %d failed, %d skipped", summary.Created, summary.Updated, summary.Failed, summary.Skipped)
	}
	return results, summary, nil
}

// persistAntigravityImport updates an existing credential (keeping groups/notes)
// or stores a new one. It does not reload the runtime.
func (h *Handler) persistAntigravityImport(ctx context.Context, existing *coreauth.Auth, entry rtImportEntry, enriched *antigravityEnrichment) (string, bool, error) {
	now := time.Now()
	refreshToken := entry.RefreshToken
	if enriched.RefreshToken != "" {
		refreshToken = enriched.RefreshToken
	}
	tokenFields := map[string]any{
		"access_token":  enriched.AccessToken,
		"refresh_token": refreshToken,
		"expires_in":    enriched.ExpiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       now.Add(time.Duration(enriched.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
	if existing != nil {
		fresh, _, errGet := h.repo.GetAuth(ctx, existing.ID)
		if errGet != nil || fresh == nil {
			fresh = existing
		}
		if fresh.Metadata == nil {
			fresh.Metadata = map[string]any{}
		}
		for key, value := range tokenFields {
			fresh.Metadata[key] = value
		}
		if enriched.ProjectID != "" {
			fresh.Metadata["project_id"] = enriched.ProjectID
		}
		if stringFromAny(fresh.Metadata["email"]) == "" && enriched.Email != "" {
			fresh.Metadata["email"] = enriched.Email
		}
		clearAccountBlockOnReauth(fresh, now)
		if enriched.EgressExplicit && enriched.EgressProxyURL != "" {
			home.ApplyEgressMetadata(fresh, home.EgressSpec{Name: enriched.EgressName, ProxyURL: enriched.EgressProxyURL, IP: enriched.EgressIP})
		}
		fresh.UpdatedAt = now.UTC()
		if _, errUpsert := h.repo.UpsertAuth(ctx, fresh, "upsert"); errUpsert != nil {
			return "", false, errUpsert
		}
		if h.runtime != nil {
			h.runtime.RequestSubscriptionCheck(fresh.ID, "re-import")
		}
		return fresh.ID, false, nil
	}
	metadata := map[string]any{}
	for key, value := range entry.Extra {
		metadata[key] = value
	}
	for key, value := range tokenFields {
		metadata[key] = value
	}
	metadata["type"] = "antigravity"
	metadata["email"] = enriched.Email
	if enriched.EgressProxyURL != "" {
		metadata["proxy_url"] = enriched.EgressProxyURL
		metadata[home.EgressIDKey] = enriched.EgressName
		metadata[home.EgressIPKey] = enriched.EgressIP
	}
	if enriched.ProjectID != "" {
		metadata["project_id"] = enriched.ProjectID
	}
	raw, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return "", false, errMarshal
	}
	updatedRaw, fileUUID, _, errUUID := cluster.EnsureOAuthPayloadUUID(raw)
	if errUUID != nil {
		return "", false, errUUID
	}
	auths := h.synthesizeOAuthPayload(updatedRaw, fileUUID, antigravityCredentialFileName(enriched.Email))
	if len(auths) == 0 {
		return "", false, fmt.Errorf("could not build credential")
	}
	if enriched.EgressProxyURL != "" {
		for _, a := range auths {
			a.ProxyURL = enriched.EgressProxyURL
		}
	}
	if errReplace := h.replaceOAuthPayloadAuths(ctx, fileUUID, auths); errReplace != nil {
		return "", false, errReplace
	}
	return auths[0].ID, true, nil
}

// antigravityPayloadNeedsImport reports whether an uploaded JSON should go through
// the enrichment + dedupe pipeline (antigravity with a refresh token).
func antigravityPayloadNeedsImport(raw []byte) (rtImportEntry, bool) {
	var value map[string]any
	if errJSON := json.Unmarshal(raw, &value); errJSON != nil {
		return rtImportEntry{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(stringFromAny(value["type"])), "antigravity") {
		return rtImportEntry{}, false
	}
	entry := entryFromJSONValue(0, value)
	if entry.RefreshToken == "" {
		return rtImportEntry{}, false
	}
	// keep the uploaded UUID / file-name style fields as extras
	return entry, true
}

// antigravityArrayPayload returns entries when raw is a JSON array (of objects or
// token strings), so one uploaded file / paste can carry many credentials.
func antigravityArrayPayload(raw []byte) ([]rtImportEntry, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	entries := parseRefreshTokenInput(string(trimmed))
	return entries, len(entries) > 0
}

// ImportRefreshTokens handles POST /credentials/import/refresh-tokens.
//
// Body: {"text": "<tokens or JSON>", "items": [...], "dry_run": bool, "concurrency": n}
// At most rtImportMaxItemsPerRequest entries per call; the panel sends chunks.
func (h *Handler) ImportRefreshTokens(c *gin.Context) {
	var body struct {
		Text         string `json:"text"`
		Items        []any  `json:"items"`
		DryRun       bool   `json:"dry_run"`
		Concurrency  int    `json:"concurrency"`
		EgressID     string `json:"egress_id"`
		AllowPrimary *bool  `json:"allow_primary"`
		Reserve      bool   `json:"reserve"`
		HealthCheck  *bool  `json:"health_check"`
		BatchID      string `json:"batch_id"`
	}
	if errBind := json.NewDecoder(io.LimitReader(c.Request.Body, 8<<20)).Decode(&body); errBind != nil {
		respondError(c, http.StatusBadRequest, "invalid_body", fmt.Errorf("body must be JSON: {text, items, dry_run}"))
		return
	}
	entries := parseRefreshTokenInput(body.Text)
	for _, item := range body.Items {
		entries = append(entries, entryFromJSONValue(len(entries), item))
	}
	if len(entries) == 0 {
		respondError(c, http.StatusBadRequest, "empty", fmt.Errorf("no refresh tokens found in input"))
		return
	}
	if len(entries) > rtImportMaxItemsPerRequest {
		respondError(c, http.StatusRequestEntityTooLarge, "too_many_items", fmt.Errorf("at most %d tokens per request; split the batch", rtImportMaxItemsPerRequest))
		return
	}
	ctx, cancel := context.WithTimeout(requestContextOrBackground(c), 5*time.Minute)
	defer cancel()
	results, summary, errImport := h.importAntigravityEntries(ctx, entries, rtImportOptions{DryRun: body.DryRun, Concurrency: body.Concurrency, Egress: body.EgressID, AllowPrimary: body.AllowPrimary, Reserve: body.Reserve})
	if errImport != nil {
		respondError(c, http.StatusInternalServerError, "import_failed", errImport)
		return
	}
	resp := gin.H{"status": "ok", "dry_run": body.DryRun, "summary": summary, "results": results}
	if !body.DryRun {
		// Record the import batch of every newly created credential (warranty evidence),
		// then health-check them in the background; the dialog polls the batch summary.
		batch := strings.TrimSpace(body.BatchID)
		if batch == "" || len(batch) > 64 {
			batch = newHealthBatchID()
		}
		var created []string
		for _, res := range results {
			if res.Status == "created" && res.CredentialID != "" {
				created = append(created, res.CredentialID)
			}
		}
		if len(created) > 0 {
			if err := h.repo.RecordCredentialImports(ctx, created, batch, time.Now(), "refresh_token"); err != nil {
				log.Warnf("rt import: record batch %s: %v", batch, err)
			}
			check := h.runtime != nil && h.runtime.ImportCheckSettings().OnImport
			if body.HealthCheck != nil {
				check = *body.HealthCheck && h.runtime != nil
			}
			if check {
				h.startHealthChecks(created)
				resp["health_batch_id"] = batch
			}
		}
		resp["batch_id"] = batch
	}
	c.JSON(http.StatusOK, resp)
}

type egressCtxKey struct{}

// withEgressProxy makes Google calls made with ctx go through the given egress proxy.
func withEgressProxy(ctx context.Context, proxyURL string) context.Context {
	return context.WithValue(ctx, egressCtxKey{}, proxyURL)
}

// oauthHTTPClientFor uses the egress proxy carried by ctx (fail closed), else the default.
func (h *Handler) oauthHTTPClientFor(ctx context.Context) *http.Client {
	if ctx != nil {
		if proxyURL, _ := ctx.Value(egressCtxKey{}).(string); proxyURL != "" {
			transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
			if err != nil || transport == nil {
				return &http.Client{Transport: failingRoundTripper{}}
			}
			return &http.Client{Timeout: 60 * time.Second, Transport: transport}
		}
	}
	return h.oauthHTTPClient()
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("credential egress proxy unusable")
}

// egressPicker hands out egresses for one import batch: a fixed explicit choice, or
// least-loaded healthy with in-batch counting so a batch spreads across egresses.
type egressPicker struct {
	mu       sync.Mutex
	explicit *home.EgressSpec
	items    []home.EgressStatus
	// allowPrimary lets auto use node primary IPs.
	allowPrimary bool
	// maxPer is Settings MaxPerEgress (0 = unlimited).
	maxPer int
}

func (h *Handler) newEgressPicker(ctx context.Context, choice string, allowPrimary *bool) (*egressPicker, error) {
	p := &egressPicker{}
	if h.runtime == nil {
		return p, nil
	}
	if allowPrimary != nil {
		p.allowPrimary = *allowPrimary
	} else {
		p.allowPrimary = h.runtime.EgressSettings(ctx).AllowPrimary
	}
	if c := strings.TrimSpace(choice); c != "" && !strings.EqualFold(c, home.EgressAuto) {
		spec, _, err := h.runtime.ResolveEgress(ctx, c, allowPrimary)
		if err != nil {
			return nil, err
		}
		p.explicit = &spec
		return p, nil
	}
	p.maxPer = h.runtime.EgressSettings(ctx).MaxPerEgress
	p.items, _ = h.runtime.EgressStatuses(ctx)
	p.items = assignableEgressStatuses(p.items, h.runtime.EgressSettings(ctx))
	return p, nil
}

func (p *egressPicker) pick() (home.EgressSpec, bool) {
	if p == nil {
		return home.EgressSpec{}, false
	}
	if p.explicit != nil {
		return *p.explicit, true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	best := -1
	for i := range p.items {
		it := &p.items[i]
		if !it.Healthy || it.Removed || (it.Primary && !p.allowPrimary) || (p.maxPer > 0 && it.Credentials >= p.maxPer) {
			continue
		}
		if best < 0 || it.Credentials < p.items[best].Credentials || (it.Credentials == p.items[best].Credentials && it.Name < p.items[best].Name) {
			best = i
		}
	}
	if best < 0 {
		return home.EgressSpec{}, false
	}
	p.items[best].Credentials++
	return p.items[best].EgressSpec, true
}

// importEgress picks the egress for an imported token. An explicit choice wins;
// otherwise a matched credential keeps its own proxy, else the batch picker decides.
// With managed egresses and none eligible, it fails (never proceeds without an egress).
func (h *Handler) importEgress(idx *credentialIndex, mu *sync.Mutex, e rtImportEntry, picker *egressPicker) (home.EgressSpec, bool, error) {
	if picker != nil && picker.explicit != nil {
		return *picker.explicit, true, nil
	}
	mu.Lock()
	existing, _ := idx.match(e.Email, e.RefreshToken)
	mu.Unlock()
	if existing != nil {
		if p := strings.TrimSpace(existing.ProxyURL); p != "" {
			return home.EgressSpec{ProxyURL: p}, false, nil
		}
	}
	spec, ok := picker.pick()
	if !ok && picker != nil && len(picker.items) > 0 {
		return home.EgressSpec{}, false, home.NoEgressError(picker.allowPrimary)
	}
	return spec, false, nil
}

// parseAllowPrimary reads an optional allow_primary query value (nil = global default).
func parseAllowPrimary(v string) *bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return nil
	}
	b := v == "1" || v == "true" || v == "yes" || v == "on"
	return &b
}

// assignableEgressStatuses drops egresses on the disabled list (Settings).
func assignableEgressStatuses(items []home.EgressStatus, settings home.EgressSettingsValue) []home.EgressStatus {
	if len(settings.Disabled) == 0 {
		return items
	}
	out := make([]home.EgressStatus, 0, len(items))
	for _, it := range items {
		if !settings.IsDisabled(it.EgressSpec) {
			out = append(out, it)
		}
	}
	return out
}
