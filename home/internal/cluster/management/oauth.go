package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/watcher/synthesizer"
)

// ListAuthFiles returns an auth files.
func (h *Handler) ListAuthFiles(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	auths, errAuths := h.repo.ListAuths(ctx)
	if errAuths != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuths)
		return
	}
	observation, errObservation := h.repo.ReadInFlightObservation(ctx, h.inFlightStaleAfter())
	var observationRead *cluster.InFlightObservationReadModel
	if errObservation == nil {
		observationRead = &observation
	}
	states, errStates := h.repo.ReadConcurrencyState(ctx)
	limiterAvailable := errStates == nil
	observedCredentials := map[string]cluster.InFlightObservedCredentialItem{}
	if observationRead != nil {
		observedCredentials = observedCredentialsByID(*observationRead)
	}
	statesByCredential := make(map[string]cluster.CredentialConcurrencyState, len(states))
	for index := range states {
		state := states[index]
		statesByCredential[state.CredentialID] = state
	}
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if !isOAuthAuth(auth) {
			continue
		}
		entry := authFileEntry(auth)
		observed, observedOK := observedCredentials[auth.ID]
		if observationRead != nil && observedOK {
			applyInFlightObservationToAuthFile(entry, *observationRead, observed)
		} else {
			applyNoInFlightObservationToAuthFile(entry)
		}
		if limiterAvailable {
			applyConcurrencyStateToAuthFile(entry, statesByCredential[auth.ID], observationRead, observed, observedOK)
		}
		files = append(files, entry)
	}
	h.annotateQuotaProbes(c.Request.Context(), files)
	sort.Slice(files, func(i, j int) bool {
		return fmt.Sprint(files[i]["name"]) < fmt.Sprint(files[j]["name"])
	})
	var observedAt any
	if observationRead != nil {
		observedAt = inFlightObservedAt(*observationRead)
	}
	c.JSON(http.StatusOK, gin.H{"in_flight_observed_at": observedAt, "files": files})
}

// DownloadAuthFile downloads an auth file.
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	auth, errAuth := h.findOAuthAuth(ctx, authIdentifierFromRequest(c))
	if errAuth != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuth)
		return
	}
	if auth == nil {
		respondError(c, http.StatusNotFound, "not_found", nil)
		return
	}
	data, errMarshal := json.MarshalIndent(auth.Metadata, "", "  ")
	if errMarshal != nil {
		respondError(c, http.StatusInternalServerError, "marshal_failed", errMarshal)
		return
	}
	data = append(data, '\n')
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", authFileName(auth)))
	_, _ = c.Writer.Write(data)
}

// UploadAuthFile handles upload auth file.
func (h *Handler) UploadAuthFile(c *gin.Context) {
	// Antigravity payloads with a refresh token go through enrichment + dedupe;
	// other payloads are stored as before. Multi-file uploads continue past bad
	// files and return a per-file report.
	dryRun := strings.EqualFold(strings.TrimSpace(c.Query("dry_run")), "true") || strings.TrimSpace(c.Query("dry_run")) == "1"
	if strings.HasPrefix(strings.ToLower(c.ContentType()), "multipart/form-data") {
		headers, errHeaders := multipartHeaders(c)
		if errHeaders != nil {
			respondError(c, http.StatusBadRequest, "invalid multipart", errHeaders)
			return
		}
		if len(headers) == 0 {
			respondError(c, http.StatusBadRequest, "no files uploaded", nil)
			return
		}
		ctx, cancel := context.WithTimeout(requestContextOrBackground(c), 5*time.Minute)
		defer cancel()
		results := make([]RTImportResult, 0, len(headers))
		uploaded := make([]string, 0, len(headers))
		var agEntries []rtImportEntry
		var agPos []int
		for i, header := range headers {
			name := ""
			if header != nil {
				name = filepath.Base(header.Filename)
			}
			res := RTImportResult{Index: i, Source: name}
			data, errRead := readUploadedJSON(header)
			if errRead != nil {
				res.Status, res.Code, res.Reason = "failed", "invalid_file", errRead.Error()
				results = append(results, res)
				continue
			}
			if many, ok := antigravityArrayPayload(data); ok {
				for k := range many {
					many[k].Source = fmt.Sprintf("%s #%d", name, k+1)
					agEntries = append(agEntries, many[k])
					agPos = append(agPos, len(results))
					results = append(results, RTImportResult{Index: len(results), Source: many[k].Source})
				}
				continue
			}
			if entry, ok := antigravityPayloadNeedsImport(data); ok {
				entry.Index, entry.Source = i, name
				agEntries = append(agEntries, entry)
				agPos = append(agPos, len(results))
				results = append(results, res)
				continue
			}
			if dryRun {
				res.Status = "would_create"
				results = append(results, res)
				continue
			}
			stored, errStore := h.storeOAuthPayloadWithContext(ctx, data, header.Filename)
			if errStore != nil {
				res.Status, res.Code, res.Reason = "failed", "upload_failed", shortReason(errStore.Error())
			} else {
				res.Status, res.CredentialID = "created", strings.TrimSuffix(stored, ".json")
				uploaded = append(uploaded, stored)
			}
			results = append(results, res)
		}
		if len(agEntries) > 0 {
			agResults, _, errImport := h.importAntigravityEntries(ctx, agEntries, rtImportOptions{DryRun: dryRun, Egress: c.Query("egress_id"), AllowPrimary: parseAllowPrimary(c.Query("allow_primary"))})
			if errImport != nil {
				respondError(c, http.StatusInternalServerError, "import_failed", errImport)
				return
			}
			for k, r := range agResults {
				results[agPos[k]] = r
				if r.Status == "created" || r.Status == "updated" {
					uploaded = append(uploaded, r.CredentialID+".json")
				}
			}
		}
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
		status, code := "ok", http.StatusOK
		if summary.Failed > 0 {
			status = "partial"
			if summary.Failed == summary.Total {
				status, code = "failed", http.StatusBadRequest
			}
		}
		c.JSON(code, gin.H{"status": status, "dry_run": dryRun, "uploaded": len(uploaded), "files": uploaded, "summary": summary, "results": results})
		return
	}
	body, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		respondError(c, http.StatusBadRequest, "invalid body", errRead)
		return
	}
	if many, ok := antigravityArrayPayload(body); ok {
		if len(many) > rtImportMaxItemsPerRequest*5 {
			respondError(c, http.StatusRequestEntityTooLarge, "too_many_items", fmt.Errorf("at most %d entries per upload", rtImportMaxItemsPerRequest*5))
			return
		}
		ctx, cancel := context.WithTimeout(requestContextOrBackground(c), 5*time.Minute)
		defer cancel()
		results, summary, errImport := h.importAntigravityEntries(ctx, many, rtImportOptions{DryRun: dryRun, Egress: c.Query("egress_id"), AllowPrimary: parseAllowPrimary(c.Query("allow_primary"))})
		if errImport != nil {
			respondError(c, http.StatusInternalServerError, "import_failed", errImport)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "dry_run": dryRun, "summary": summary, "results": results})
		return
	}
	if entry, ok := antigravityPayloadNeedsImport(bytes.TrimSpace(body)); ok {
		ctx, cancel := context.WithTimeout(requestContextOrBackground(c), 2*time.Minute)
		defer cancel()
		results, _, errImport := h.importAntigravityEntries(ctx, []rtImportEntry{entry}, rtImportOptions{DryRun: dryRun, Egress: c.Query("egress_id"), AllowPrimary: parseAllowPrimary(c.Query("allow_primary"))})
		if errImport != nil {
			respondError(c, http.StatusInternalServerError, "import_failed", errImport)
			return
		}
		r := results[0]
		if r.Status == "failed" || r.Status == "skipped" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "upload_failed", "code": r.Code, "message": r.Reason, "result": r})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "dry_run": dryRun, "name": r.CredentialID + ".json", "result": r})
		return
	}
	if dryRun {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "dry_run": true})
		return
	}
	name, errStore := h.storeOAuthPayload(c, body, "")
	if errStore != nil {
		respondError(c, http.StatusBadRequest, "upload_failed", errStore)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "name": name})
}

// readUploadedJSON reads one uploaded .json file.
func readUploadedJSON(header *multipart.FileHeader) ([]byte, error) {
	if header == nil {
		return nil, fmt.Errorf("file is required")
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Base(header.Filename)), ".json") {
		return nil, fmt.Errorf("file must be .json")
	}
	src, errOpen := header.Open()
	if errOpen != nil {
		return nil, errOpen
	}
	defer func() { _ = src.Close() }()
	data, errRead := io.ReadAll(io.LimitReader(src, 4<<20))
	if errRead != nil {
		return nil, errRead
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("file is not valid JSON")
	}
	return data, nil
}

// DeleteAuthFile deletes an auth file.
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	// Validate request inputs before mutating persisted state.
	ctx, cancel := h.requestContext(c)
	defer cancel()
	auths, errAuths := h.repo.ListAuths(ctx)
	if errAuths != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuths)
		return
	}
	if all := strings.TrimSpace(c.Query("all")); all == "1" || strings.EqualFold(all, "true") || all == "*" {
		deleted := 0
		for _, auth := range auths {
			if !isOAuthAuth(auth) {
				continue
			}
			if errDelete := h.repo.SoftDeleteAuth(ctx, auth.ID); errDelete != nil {
				respondError(c, http.StatusInternalServerError, "write_failed", errDelete)
				return
			}
			deleted++
		}
		if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
			respondError(c, http.StatusInternalServerError, "reload_failed", errRefresh)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": deleted})
		return
	}

	auth := findOAuthAuthInList(auths, authIdentifierFromRequest(c))
	if auth == nil {
		respondError(c, http.StatusNotFound, "not_found", nil)
		return
	}
	if errDelete := h.deleteOAuthAuth(ctx, auth); errDelete != nil {
		respondError(c, http.StatusInternalServerError, "write_failed", errDelete)
		return
	}
	if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
		respondError(c, http.StatusInternalServerError, "reload_failed", errRefresh)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PatchAuthFileStatus applies a partial update to an auth file status.
func (h *Handler) PatchAuthFileStatus(c *gin.Context) {
	// Validate request inputs before mutating persisted state.
	var req struct {
		Name     string `json:"name"`
		Disabled *bool  `json:"disabled"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	auth, errAuth := h.findOAuthAuth(ctx, authIdentifier{ID: name, Name: name})
	if errAuth != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuth)
		return
	}
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	disabled := *req.Disabled
	applyAuthDisabledStatus(auth, disabled)
	if _, errUpsert := h.repo.UpsertAuth(ctx, auth, "update"); errUpsert != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", errUpsert)})
		return
	}
	if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", errRefresh)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": disabled})
}

// PatchAuthFileFields applies a partial update to an auth file fields.
func (h *Handler) PatchAuthFileFields(c *gin.Context) {
	// Validate request inputs before mutating persisted state.
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	if errDecode := decoder.Decode(&body); errDecode != nil {
		respondError(c, http.StatusBadRequest, "invalid body", errDecode)
		return
	}
	values, errValues := decodeOAuthFieldPatchValues(body)
	if errValues != nil {
		respondError(c, http.StatusBadRequest, "invalid body", errValues)
		return
	}
	concurrencyPatch, errConcurrencyPatch := decodeConcurrencyPolicyPatchRequest(body)
	if errConcurrencyPatch != nil {
		respondError(c, http.StatusBadRequest, "invalid body", errConcurrencyPatch)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	auth, errAuth := h.findOAuthAuth(ctx, authIdentifierFromBodyAndRequest(c, values))
	if errAuth != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", errAuth)
		return
	}
	if auth == nil {
		respondError(c, http.StatusNotFound, "not_found", nil)
		return
	}
	removeOAuthFieldPatchIdentifierFields(body)
	removeOAuthFieldPatchConcurrencyFields(body)
	egressChanged, errEgress := h.applyEgressPatch(ctx, auth, body)
	if errEgress != nil {
		respondError(c, http.StatusBadRequest, "invalid egress", errEgress)
		return
	}
	changed := egressChanged
	if len(body) > 0 {
		prevProxy := strings.TrimSpace(auth.ProxyURL)
		fieldsChanged, errPatch := applyOAuthFieldPatch(auth, body)
		if errPatch != nil {
			respondError(c, http.StatusBadRequest, "invalid body", errPatch)
			return
		}
		changed = changed || fieldsChanged
		_, custom := body["proxy_url"]
		if _, alias := body["proxy-url"]; alias {
			custom = true
		}
		if custom && !egressChanged && strings.TrimSpace(auth.ProxyURL) != prevProxy {
			// A custom URL replaces the managed egress pin.
			home.ApplyEgressMetadata(auth, home.EgressSpec{ProxyURL: auth.ProxyURL})
			delete(auth.Metadata, home.EgressIDKey)
			delete(auth.Metadata, home.EgressIPKey)
		}
	}
	policyChanged := concurrencyPatch.MaxInFlight.Set || concurrencyPatch.MaxInFlightByModel.Set
	if !changed && !policyChanged {
		respondError(c, http.StatusBadRequest, "no fields to update", nil)
		return
	}
	if changed {
		auth.UpdatedAt = time.Now().UTC()
	}
	errWrite := h.repo.PatchAuthAndConcurrency(ctx, cluster.AuthConcurrencyPatchRequest{
		CredentialID:          auth.ID,
		Auth:                  auth,
		AuthChanged:           changed,
		PolicyPatch:           cluster.ConcurrencyPolicyPatch{MaxInFlight: concurrencyPatch.MaxInFlight, MaxInFlightByModel: concurrencyPatch.MaxInFlightByModel},
		ExpectedPolicyVersion: concurrencyPatch.Version,
	})
	if errWrite != nil {
		respondConcurrencyPolicyError(c, errWrite)
		return
	}
	if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
		respondError(c, http.StatusInternalServerError, "reload_failed", errRefresh)
		return
	}
	respondOK(c)
}

// PostOAuthCallback handles a post o auth callback.
func (h *Handler) PostOAuthCallback(c *gin.Context) {
	h.handleOAuthCallback(c)
}

// storeUploadedOAuth stores an uploaded o auth.
func (h *Handler) storeUploadedOAuth(c *gin.Context, header *multipart.FileHeader) (string, error) {
	if header == nil {
		return "", fmt.Errorf("file is required")
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Base(header.Filename)), ".json") {
		return "", fmt.Errorf("file must be .json")
	}
	src, errOpen := header.Open()
	if errOpen != nil {
		return "", errOpen
	}
	data, errRead := io.ReadAll(src)
	if errClose := src.Close(); errClose != nil && errRead == nil {
		errRead = errClose
	}
	if errRead != nil {
		return "", errRead
	}
	return h.storeOAuthPayload(c, data, header.Filename)
}

// storeOAuthPayload stores an o auth payload.
func (h *Handler) storeOAuthPayload(c *gin.Context, raw []byte, originalFilename string) (string, error) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	return h.storeOAuthPayloadWithContext(ctx, raw, originalFilename)
}

// storeOAuthPayloadWithContext stores an o auth payload with context.
func (h *Handler) storeOAuthPayloadWithContext(ctx context.Context, raw []byte, originalFilename string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("empty credential json")
	}
	updatedRaw, fileUUID, _, errUUID := cluster.EnsureOAuthPayloadUUID(raw)
	if errUUID != nil {
		return "", errUUID
	}

	auths := h.synthesizeOAuthPayload(updatedRaw, fileUUID, originalFilename)
	if len(auths) == 0 {
		return "", fmt.Errorf("unsupported credential json")
	}
	if state, ok := ctx.Value(oauthSessionStateKey{}).(string); ok && state != "" {
		result := map[string]any{"credential_id": fileUUID, "updated_existing": false}
		if len(auths) > 0 && auths[0] != nil {
			result["email"] = stringFromAny(auths[0].Metadata["email"])
		}
		if errComplete := h.repo.CompleteOAuthSessionWithAuthsResult(ctx, state, auths, result); errComplete != nil {
			return "", errComplete
		}
	} else if errReplace := h.replaceOAuthPayloadAuths(ctx, fileUUID, auths); errReplace != nil {
		return "", errReplace
	}
	if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
		return "", errRefresh
	}
	return fileUUID + ".json", nil
}

// synthesizeOAuthPayload handles a synthesize o auth payload.
func (h *Handler) synthesizeOAuthPayload(raw []byte, fileUUID string, originalFilename string) []*coreauth.Auth {
	// Resolve credential context before calling upstream OAuth services.
	cfg := h.runtime.Config()
	authPath := fileUUID + ".json"
	sctx := &synthesizer.SynthesisContext{
		Config:      cfg,
		AuthDir:     "",
		Now:         time.Now().UTC(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
		ClusterMode: true,
	}
	sctx.UUIDForAuth = func(auth *coreauth.Auth) string {
		_ = auth
		return fileUUID
	}
	auths := synthesizer.SynthesizeAuthFile(sctx, authPath, raw)
	cluster.ApplyOriginalAuthFileName(auths, originalFilename)
	return auths
}

// replaceOAuthPayloadAuths handles a replace o auth payload auths.
func (h *Handler) replaceOAuthPayloadAuths(ctx context.Context, fileUUID string, auths []*coreauth.Auth) error {
	// Resolve credential context before calling upstream OAuth services.
	nextIDs := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		nextIDs[auth.ID] = struct{}{}
		if _, errUpsert := h.repo.UpsertAuth(ctx, auth, "upsert"); errUpsert != nil {
			return errUpsert
		}
	}

	existingAuths, errExisting := h.repo.ListAuths(ctx)
	if errExisting != nil {
		return errExisting
	}
	for _, existing := range existingAuths {
		if !isOAuthPayloadAuth(existing, fileUUID) {
			continue
		}
		if _, ok := nextIDs[existing.ID]; ok {
			continue
		}
		if errDelete := h.repo.SoftDeleteAuth(ctx, existing.ID); errDelete != nil {
			return errDelete
		}
	}
	return nil
}

// isOAuthPayloadAuth reports whether o auth payload auth.
func isOAuthPayloadAuth(auth *coreauth.Auth, fileUUID string) bool {
	fileUUID = strings.TrimSpace(fileUUID)
	if auth == nil || fileUUID == "" {
		return false
	}
	if strings.TrimSpace(auth.ID) == fileUUID {
		return true
	}
	return false
}

// findOAuthAuth handles a find o auth auth.
func (h *Handler) findOAuthAuth(ctx context.Context, identifier authIdentifier) (*coreauth.Auth, error) {
	auths, errAuths := h.repo.ListAuths(ctx)
	if errAuths != nil {
		return nil, errAuths
	}
	return findOAuthAuthInList(auths, identifier), nil
}

// deleteOAuthAuth deletes an o auth auth.
func (h *Handler) deleteOAuthAuth(ctx context.Context, auth *coreauth.Auth) error {
	if auth == nil {
		return nil
	}
	if errDelete := h.repo.SoftDeleteAuth(ctx, auth.ID); errDelete != nil {
		return errDelete
	}
	return nil
}

type authIdentifier struct {
	ID    string
	Name  string
	Index *int
}

// authIdentifierFromRequest derives auth identifier from request.
func authIdentifierFromRequest(c *gin.Context) authIdentifier {
	identifier := authIdentifier{
		ID:   firstNonEmptyQuery(c, "id", "uuid", "auth_index"),
		Name: firstNonEmptyQuery(c, "name", "file", "filename"),
	}
	if idxRaw := strings.TrimSpace(c.Query("index")); idxRaw != "" {
		if idx, errAtoi := strconv.Atoi(idxRaw); errAtoi == nil {
			identifier.Index = &idx
		}
	}
	return identifier
}

// authIdentifierFromBodyAndRequest derives auth identifier from body and request.
func authIdentifierFromBodyAndRequest(c *gin.Context, body map[string]any) authIdentifier {
	identifier := authIdentifierFromRequest(c)
	for _, key := range []string{"id", "uuid", "auth_index", "index"} {
		if identifier.ID == "" {
			identifier.ID = stringFromAny(body[key])
		}
	}
	if identifier.Name == "" {
		for _, key := range []string{"name", "file", "filename"} {
			identifier.Name = stringFromAny(body[key])
			if identifier.Name != "" {
				break
			}
		}
	}
	return identifier
}

// findOAuthAuthInList handles a find o auth auth in list.
func findOAuthAuthInList(auths []*coreauth.Auth, identifier authIdentifier) *coreauth.Auth {
	// Resolve credential context before calling upstream OAuth services.
	filtered := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if isOAuthAuth(auth) {
			filtered = append(filtered, auth)
		}
	}
	if identifier.Index != nil && *identifier.Index >= 0 && *identifier.Index < len(filtered) {
		return filtered[*identifier.Index]
	}
	for _, auth := range filtered {
		if identifier.ID != "" && (auth.ID == identifier.ID || auth.Index == identifier.ID) {
			return auth
		}
	}
	if identifier.Name != "" {
		for _, auth := range filtered {
			if authFileDisplayName(auth) == identifier.Name {
				return auth
			}
		}
		for _, auth := range filtered {
			if authFileName(auth) == identifier.Name {
				return auth
			}
		}
	}
	for _, auth := range filtered {
		if identifier.Name != "" && authFileName(auth) == identifier.Name {
			return auth
		}
	}
	return nil
}

// isOAuthAuth reports whether o auth auth.
func isOAuthAuth(auth *coreauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if auth.Attributes != nil && strings.HasPrefix(strings.TrimSpace(auth.Attributes["source"]), "config:") {
		return false
	}
	typeValue := stringFromAny(auth.Metadata["type"])
	return typeValue != ""
}

// applyAuthDisabledStatus updates disabled status fields and persisted metadata.
func applyAuthDisabledStatus(auth *coreauth.Auth, disabled bool) {
	if auth == nil {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "disabled via management API"
	} else {
		auth.Status = coreauth.StatusActive
		auth.StatusMessage = ""
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = disabled
	// Any manual toggle takes over from an egress auto-pause (a still-down egress re-pauses).
	delete(auth.Metadata, "auto_paused_reason")
	if !disabled {
		// Manual re-enable lifts an account-403 auto-disable; older 403 states are ignored.
		delete(auth.Metadata, "auto_disabled_reason")
		delete(auth.Metadata, "auto_disabled_at")
		auth.Metadata["auto_disabled_cleared_at"] = time.Now().UTC().Format(time.RFC3339)
		auth.LastError = nil
	}
	auth.UpdatedAt = time.Now()
}

// authFileEntry handles an auth file entry.
func authFileEntry(auth *coreauth.Auth) gin.H {
	// Validate request inputs before mutating persisted state.
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.ID,
		"name":           authFileDisplayName(auth),
		"file_name":      authFileName(auth),
		"type":           auth.Provider,
		"provider":       auth.Provider,
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable && !onlyModelScopedCooldown(auth),
		"runtime_only":   auth.Attributes != nil && strings.EqualFold(auth.Attributes["runtime_only"], "true"),
		"source":         "db",
		"prefix":         prefixFromAuth(auth),
		"proxy_url":      proxyURLFromAuth(auth),
		"websockets":     websocketsFromAuth(auth),
	}
	if health := coreauth.ClusterHealth().Status(auth.ID); len(health) > 0 {
		entry["health"] = health
	}
	if email := stringFromAny(auth.Metadata["email"]); email != "" {
		entry["email"] = email
	}
	if reason := stringFromAny(auth.Metadata["auto_disabled_reason"]); reason != "" {
		entry["auto_disabled_reason"] = reason
	}
	if reason := stringFromAny(auth.Metadata["auto_paused_reason"]); reason != "" && auth.Disabled {
		entry["auto_paused_reason"] = reason
	}
	if tier := stringFromAny(auth.Metadata["subscription_tier"]); tier != "" {
		entry["subscription_tier"] = tier
		entry["subscription_tier_name"] = stringFromAny(auth.Metadata["subscription_tier_name"])
		entry["subscription_checked_at"] = stringFromAny(auth.Metadata["subscription_checked_at"])
	}
	if ip := stringFromAny(auth.Metadata["egress_ip"]); ip != "" && strings.TrimSpace(auth.ProxyURL) != "" {
		entry["egress_ip"] = ip
		entry["egress_id"] = stringFromAny(auth.Metadata["egress_id"])
	}
	if priority := priorityFromAuth(auth); priority != nil {
		entry["priority"] = *priority
	}
	if note := noteFromAuth(auth); note != "" {
		entry["note"] = note
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["updated_at"] = auth.UpdatedAt
		entry["modtime"] = auth.UpdatedAt
	}
	// Surface the cooling override so the management UI can distinguish an
	// explicit false value from an absent override (mirrors apiKeyAuthToMap).
	if disabledCooling := auth.DisableCoolingOverride(); disabledCooling != nil {
		entry["disable-cooling"] = *disabledCooling
	}
	if requestRetry, ok := auth.RequestRetryOverride(); ok {
		entry["request-retry"] = requestRetry
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if cooldowns := modelCooldownEntries(auth, time.Now()); len(cooldowns) > 0 {
		entry["model_cooldowns"] = cooldowns
	}
	if credentialScopeError(auth, time.Now()) {
		entry["credential_scope_error"] = true
	}
	if auth.Unavailable && onlyModelScopedCooldown(auth) {
		// Every model tried so far is cooling, but each cooldown is model-scoped
		// (e.g. per-model 429 quota). Untried models can still work, so this is not a
		// whole-credential outage.
		entry["tried_models_all_cooling"] = true
	}
	return entry
}

// onlyModelScopedCooldown reports whether auth.Unavailable is just the aggregate of
// per-model cooldowns rather than a credential-level failure.
func onlyModelScopedCooldown(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" {
		return false
	}
	if auth.LastError != nil && (auth.LastError.HTTPStatus == 401 || auth.LastError.HTTPStatus == 403) {
		return false
	}
	for _, st := range auth.ModelStates {
		if st != nil && st.Status == coreauth.StatusDisabled {
			continue
		}
		if st == nil {
			continue
		}
		if st.LastError != nil && (st.LastError.HTTPStatus == 401 || st.LastError.HTTPStatus == 403) {
			return false
		}
		// The aggregate only goes unavailable when every model state is cooling; if one
		// is not, the unavailability was set at credential level.
		if !st.Unavailable {
			return false
		}
	}
	return true
}

// modelCooldownEntries lists models that are cooling down or recently failed on
// this credential. Model-scoped failures (per-model quota, capacity) only
// affect that (credential, model) pair; the credential stays usable for others.
func modelCooldownEntries(auth *coreauth.Auth, now time.Time) []gin.H {
	out := []gin.H{}
	if auth == nil {
		return out
	}
	names := make([]string, 0, len(auth.ModelStates))
	for name := range auth.ModelStates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		st := auth.ModelStates[name]
		if st == nil || st.Status != coreauth.StatusError {
			continue
		}
		cooling := st.Unavailable && st.NextRetryAfter.After(now)
		item := gin.H{
			"model":          name,
			"cooling":        cooling,
			"status_message": st.StatusMessage,
			"quota_exceeded": st.Quota.Exceeded,
		}
		if cooling {
			item["next_retry_after"] = st.NextRetryAfter
		}
		if st.LastError != nil && st.LastError.HTTPStatus != 0 {
			item["http_status"] = st.LastError.HTTPStatus
		}
		if st.LastError != nil && st.LastError.Code != "" {
			item["error_code"] = st.LastError.Code
		}
		if !st.UpdatedAt.IsZero() {
			item["updated_at"] = st.UpdatedAt
		}
		out = append(out, item)
	}
	return out
}

// credentialScopeError reports whether the whole credential is blocked
// (auth-level cooldown, credential quota, or 401/403), as opposed to only some
// models.
func credentialScopeError(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil || auth.Disabled {
		return false
	}
	if auth.Unavailable && !onlyModelScopedCooldown(auth) && (auth.NextRetryAfter.IsZero() || auth.NextRetryAfter.After(now)) {
		return true
	}
	if auth.LastError != nil && (auth.LastError.HTTPStatus == 401 || auth.LastError.HTTPStatus == 403) && len(auth.ModelStates) == 0 {
		return true
	}
	return false
}

// authFileName handles an auth file name.
func authFileName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if name := stringFromAny(auth.Metadata["filename"]); name != "" {
			return name
		}
	}
	return strings.TrimSpace(auth.ID) + ".json"
}

// authFileDisplayName returns a stable management-list name for the auth entry.
func authFileDisplayName(auth *coreauth.Auth) string {
	return authFileName(auth)
}

// applyOAuthFieldPatch applies an o auth field patch.
func applyOAuthFieldPatch(auth *coreauth.Auth, fields map[string]json.RawMessage) (bool, error) {
	// Resolve credential context before calling upstream OAuth services.
	normalizedFields := make(map[string]json.RawMessage, len(fields))
	pathOwners := make(map[string]string, len(fields))
	canonicalOwners := make(map[string]bool, len(fields))
	orderedPaths := make([]string, 0, len(fields))
	for key, rawValue := range fields {
		fieldPath := strings.TrimSpace(key)
		if fieldPath == "" {
			return false, fmt.Errorf("field name is required")
		}
		parts := strings.Split(fieldPath, ".")
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
			if parts[index] == "" {
				return false, fmt.Errorf("invalid field path: %s", fieldPath)
			}
		}
		originalRoot := parts[0]
		parts[0] = canonicalOAuthMetadataKey(originalRoot)
		metadataPath := strings.Join(parts, ".")
		isCanonical := originalRoot == parts[0]

		if previousKey, exists := pathOwners[metadataPath]; exists {
			previousCanonical := canonicalOwners[metadataPath]
			if previousCanonical != isCanonical {
				if isCanonical {
					normalizedFields[metadataPath] = rawValue
					pathOwners[metadataPath] = key
					canonicalOwners[metadataPath] = true
				}
				continue
			}
			return false, fmt.Errorf("fields %s and %s resolve to the same metadata path", previousKey, key)
		}
		normalizedFields[metadataPath] = rawValue
		pathOwners[metadataPath] = key
		canonicalOwners[metadataPath] = isCanonical
		orderedPaths = append(orderedPaths, metadataPath)
	}
	sort.Strings(orderedPaths)
	for parentIndex, parentPath := range orderedPaths {
		for childIndex := parentIndex + 1; childIndex < len(orderedPaths); childIndex++ {
			childPath := orderedPaths[childIndex]
			if strings.HasPrefix(childPath, parentPath+".") {
				return false, fmt.Errorf("fields %s and %s overlap", pathOwners[parentPath], pathOwners[childPath])
			}
		}
	}
	decodedValues := make(map[string]any, len(normalizedFields))
	for _, metadataPath := range orderedPaths {
		rawValue := normalizedFields[metadataPath]
		fieldPath := pathOwners[metadataPath]
		value, errDecode := decodeOAuthFieldPatchValue(rawValue)
		if errDecode != nil {
			return false, fmt.Errorf("invalid field %s", fieldPath)
		}
		if rootOAuthField(metadataPath) == "request_retry" {
			if metadataPath != "request_retry" {
				return false, fmt.Errorf("invalid field %s", fieldPath)
			}
			if value != nil {
				number, okNumber := value.(json.Number)
				if !okNumber {
					return false, fmt.Errorf("invalid field %s", fieldPath)
				}
				parsed, errInt := number.Int64()
				if errInt != nil {
					return false, fmt.Errorf("invalid field %s", fieldPath)
				}
				normalized := int(parsed)
				if int64(normalized) != parsed {
					return false, fmt.Errorf("invalid field %s", fieldPath)
				}
				if normalized < 0 {
					value = nil
				} else {
					value = normalized
				}
			}
		}
		decodedValues[metadataPath] = value
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	changed := false
	touchedRoots := make(map[string]struct{}, len(normalizedFields))
	for _, metadataPath := range orderedPaths {
		value := decodedValues[metadataPath]
		if root := rootOAuthField(metadataPath); root != "" {
			if legacy := legacyOAuthMetadataKey(root); legacy != "" {
				delete(auth.Metadata, legacy)
			}
		}
		if metadataPath == "request_retry" {
			if value == nil {
				delete(auth.Metadata, "request_retry")
				if root := rootOAuthField(metadataPath); root != "" {
					touchedRoots[root] = struct{}{}
				}
				changed = true
				continue
			}
		}
		if metadataPath == "headers" {
			applyOAuthHeadersPatch(auth, value)
		} else if errSet := setOAuthMetadataValue(auth.Metadata, metadataPath, value); errSet != nil {
			return false, errSet
		}
		if root := rootOAuthField(metadataPath); root != "" {
			touchedRoots[root] = struct{}{}
		}
		changed = true
	}
	if changed {
		syncOAuthMetadataFields(auth, touchedRoots)
	}
	return changed, nil
}

func decodeOAuthFieldPatchValues(fields map[string]json.RawMessage) (map[string]any, error) {
	values := make(map[string]any, len(fields))
	for key, rawValue := range fields {
		value, errDecode := decodeOAuthFieldPatchValue(rawValue)
		if errDecode != nil {
			return nil, fmt.Errorf("invalid field %s", key)
		}
		values[key] = value
	}
	return values, nil
}

func decodeOAuthFieldPatchValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, errDecode
	}
	return value, nil
}

func removeOAuthFieldPatchIdentifierFields(fields map[string]json.RawMessage) {
	for key := range fields {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "id", "uuid", "auth_index", "authindex", "index", "name", "file", "filename":
			delete(fields, key)
		}
	}
}

func removeOAuthFieldPatchConcurrencyFields(fields map[string]json.RawMessage) {
	for key := range fields {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "version", "max_in_flight", "max_in_flight_by_model":
			delete(fields, key)
		}
	}
}

func canonicalOAuthMetadataKey(key string) string {
	switch key {
	case "api-key":
		return "api_key"
	case "base-url":
		return "base_url"
	case "disable-cooling":
		return "disable_cooling"
	case "excluded-models":
		return "excluded_models"
	case "fingerprint-profile":
		return "fingerprint_profile"
	case "model-aliases":
		return "model_aliases"
	case "proxy-url":
		return "proxy_url"
	case "request-retry":
		return "request_retry"
	case "request-scoped-errors":
		return "request_scoped_errors"
	case "tool-prefix-disabled":
		return "tool_prefix_disabled"
	default:
		return key
	}
}

func legacyOAuthMetadataKey(canonicalKey string) string {
	switch canonicalKey {
	case "api_key":
		return "api-key"
	case "base_url":
		return "base-url"
	case "disable_cooling":
		return "disable-cooling"
	case "excluded_models":
		return "excluded-models"
	case "fingerprint_profile":
		return "fingerprint-profile"
	case "model_aliases":
		return "model-aliases"
	case "proxy_url":
		return "proxy-url"
	case "request_retry":
		return "request-retry"
	case "request_scoped_errors":
		return "request-scoped-errors"
	case "tool_prefix_disabled":
		return "tool-prefix-disabled"
	default:
		return ""
	}
}

func rootOAuthField(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "."); idx >= 0 {
		return strings.TrimSpace(path[:idx])
	}
	return path
}

func setOAuthMetadataValue(metadata map[string]any, path string, value any) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	parts := strings.Split(path, ".")
	current := metadata
	for i, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return fmt.Errorf("invalid field path: %s", path)
		}
		if i == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	return nil
}

func applyOAuthHeadersPatch(auth *coreauth.Auth, value any) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	headers, ok := oauthHeadersStringMap(value)
	if !ok {
		auth.Metadata["headers"] = value
		return
	}
	currentHeaders := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata)
	nextHeaders := make(map[string]string, len(currentHeaders))
	for name, value := range currentHeaders {
		nextHeaders[name] = value
	}
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		headerValue := strings.TrimSpace(value)
		if headerValue == "" {
			delete(nextHeaders, name)
			continue
		}
		nextHeaders[name] = headerValue
	}
	if len(nextHeaders) == 0 {
		delete(auth.Metadata, "headers")
		return
	}
	metadataHeaders := make(map[string]any, len(nextHeaders))
	for name, value := range nextHeaders {
		metadataHeaders[name] = value
	}
	auth.Metadata["headers"] = metadataHeaders
}

func oauthHeadersStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		headers := make(map[string]string, len(typed))
		for name, rawValue := range typed {
			headerValue, ok := rawValue.(string)
			if !ok {
				return nil, false
			}
			headers[name] = headerValue
		}
		return headers, true
	default:
		return nil, false
	}
}

func syncOAuthMetadataFields(auth *coreauth.Auth, touchedRoots map[string]struct{}) {
	if auth == nil || len(touchedRoots) == 0 {
		return
	}
	if _, ok := touchedRoots["prefix"]; ok {
		auth.Prefix = stringFromAny(auth.Metadata["prefix"])
	}
	if _, ok := touchedRoots["proxy_url"]; ok {
		auth.ProxyURL = stringFromAny(auth.Metadata["proxy_url"])
	}
	if _, ok := touchedRoots["headers"]; ok {
		syncOAuthHeaderAttributes(auth)
	}
	if _, ok := touchedRoots["priority"]; ok {
		syncOAuthPriorityAttribute(auth)
	}
	if _, ok := touchedRoots["note"]; ok {
		syncOAuthNoteAttribute(auth)
	}
	if _, ok := touchedRoots["websockets"]; ok {
		syncOAuthWebsocketsAttribute(auth)
	}
	if _, ok := touchedRoots["disabled"]; ok {
		syncOAuthDisabledState(auth)
	}
}

func syncOAuthHeaderAttributes(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	for key := range auth.Attributes {
		if strings.HasPrefix(key, "header:") {
			delete(auth.Attributes, key)
		}
	}
	for name, value := range coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata) {
		auth.Attributes["header:"+name] = value
	}
}

func syncOAuthPriorityAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	priority, ok := oauthIntValue(auth.Metadata["priority"])
	if !ok || priority == 0 {
		delete(auth.Attributes, "priority")
		return
	}
	auth.Attributes["priority"] = strconv.Itoa(priority)
}

func oauthIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if i, errInt := typed.Int64(); errInt == nil {
			return int(i), true
		}
	case string:
		if i, errAtoi := strconv.Atoi(strings.TrimSpace(typed)); errAtoi == nil {
			return i, true
		}
	}
	return 0, false
}

func syncOAuthNoteAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	note, ok := auth.Metadata["note"].(string)
	if !ok {
		delete(auth.Attributes, "note")
		return
	}
	note = strings.TrimSpace(note)
	if note == "" {
		delete(auth.Attributes, "note")
		return
	}
	auth.Attributes["note"] = note
}

func syncOAuthWebsocketsAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	websockets, ok := oauthBoolValue(auth.Metadata["websockets"])
	if !ok {
		delete(auth.Attributes, "websockets")
		return
	}
	auth.Attributes["websockets"] = strconv.FormatBool(websockets)
}

func oauthBoolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func syncOAuthDisabledState(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	disabled, ok := oauthBoolValue(auth.Metadata["disabled"])
	if !ok {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		if strings.TrimSpace(auth.StatusMessage) == "" {
			auth.StatusMessage = "disabled via management API"
		}
		return
	}
	auth.Status = coreauth.StatusActive
	auth.StatusMessage = ""
}

// multipartHeaders handles a multipart headers.
func multipartHeaders(c *gin.Context) ([]*multipart.FileHeader, error) {
	form, errForm := c.MultipartForm()
	if errForm != nil {
		return nil, errForm
	}
	if form == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers, nil
}

// stringFromAny derives string from any.
func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

// priorityFromAuth derives priority from auth.
func priorityFromAuth(auth *coreauth.Auth) *int {
	if auth == nil {
		return nil
	}
	if auth.Attributes != nil {
		if priorityRaw := strings.TrimSpace(auth.Attributes["priority"]); priorityRaw != "" {
			if priority, errAtoi := strconv.Atoi(priorityRaw); errAtoi == nil {
				return &priority
			}
		}
	}
	if auth.Metadata != nil {
		if priorityRaw, ok := auth.Metadata["priority"].(float64); ok {
			priority := int(priorityRaw)
			return &priority
		}
	}
	return nil
}

func prefixFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		return prefix
	}
	if auth.Metadata != nil {
		return stringFromAny(auth.Metadata["prefix"])
	}
	return ""
}

func proxyURLFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		return proxyURL
	}
	if auth.Metadata != nil {
		if proxyURL := stringFromAny(auth.Metadata["proxy_url"]); proxyURL != "" {
			return proxyURL
		}
		return stringFromAny(auth.Metadata["proxy-url"])
	}
	return ""
}

func websocketsFromAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if websockets, ok := oauthBoolValue(auth.Attributes["websockets"]); ok {
			return websockets
		}
	}
	if auth.Metadata != nil {
		if websockets, ok := oauthBoolValue(auth.Metadata["websockets"]); ok {
			return websockets
		}
	}
	return false
}

// noteFromAuth derives note from auth.
func noteFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if note := strings.TrimSpace(auth.Attributes["note"]); note != "" {
			return note
		}
	}
	if auth.Metadata != nil {
		return stringFromAny(auth.Metadata["note"])
	}
	return ""
}

// applyEgressPatch handles "egress_id" ("auto" = unpin, re-balanced within seconds;
// otherwise an egress name/IP) and removes it from body.
func (h *Handler) applyEgressPatch(ctx context.Context, auth *coreauth.Auth, body map[string]json.RawMessage) (bool, error) {
	raw, ok := body["egress_id"]
	if !ok {
		return false, nil
	}
	delete(body, "egress_id")
	var allowPrimary *bool
	if rawAP, okAP := body["allow_primary"]; okAP {
		delete(body, "allow_primary")
		var b bool
		if err := json.Unmarshal(rawAP, &b); err != nil {
			return false, fmt.Errorf("allow_primary must be a boolean")
		}
		allowPrimary = &b
	}
	var choice string
	if err := json.Unmarshal(raw, &choice); err != nil {
		return false, fmt.Errorf("egress_id must be a string")
	}
	choice = strings.TrimSpace(choice)
	if h.runtime == nil {
		if choice == "" || strings.EqualFold(choice, home.EgressAuto) {
			home.ApplyEgressMetadata(auth, home.EgressSpec{})
			return true, nil
		}
		return false, fmt.Errorf("egress management unavailable")
	}
	// auto resolves now, so the secondary-only policy is applied (and enforced) immediately.
	spec, _, err := h.runtime.ResolveEgress(ctx, choice, allowPrimary)
	if err != nil {
		return false, err
	}
	home.ApplyEgressMetadata(auth, spec)
	return true, nil
}
