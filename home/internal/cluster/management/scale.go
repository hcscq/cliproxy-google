package management

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

// Scale-oriented management endpoints: server-side search / filter /
// pagination, bulk membership changes (single transaction), rule-based
// credential group membership, server-side availability summaries and
// import / export of group membership.

const (
	credStatusOK       = "ok"
	credStatusCooling  = "cooling"
	credStatusError    = "error"
	credStatusDisabled = "disabled"
)

func credentialStatus(a *coreauth.Auth, now time.Time) string {
	switch {
	case a.Disabled:
		return credStatusDisabled
	// A raw Status==error left behind by a model-scoped failure (e.g. one
	// model's 429 quota) does not make the credential unusable; only a
	// credential-scope error or whole-credential unavailability does.
	case credentialScopeError(a, now) || (a.Unavailable && (a.NextRetryAfter.IsZero() || a.NextRetryAfter.After(now))):
		return credStatusError
	case coolingModelCount(a, now) > 0:
		return credStatusCooling
	default:
		return credStatusOK
	}
}

// liveAuths lists credentials from the database (full metadata and persisted
// runtime state), like the classic credential list.
func (h *Handler) liveAuths(ctx context.Context) []*coreauth.Auth {
	if h == nil || h.repo == nil {
		return nil
	}
	list, err := h.repo.ListAuths(ctx)
	if err != nil {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		li, lj := strings.ToLower(list[i].Label), strings.ToLower(list[j].Label)
		if li != lj {
			return li < lj
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func pageParams(c *gin.Context, def, max int) (page, size int) {
	page, _ = strconv.Atoi(c.Query("page"))
	size, _ = strconv.Atoi(c.Query("page_size"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = def
	}
	if size > max {
		size = max
	}
	return
}

func csvSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

type credFilter struct {
	q, domain, tag, group string
	egress                string // pinned egress id (exact)
	providers, statuses   map[string]bool
	exclude               uint // credential group id whose effective members are excluded
}

func credFilterFromQuery(c *gin.Context) credFilter {
	f := credFilter{
		q: strings.ToLower(strings.TrimSpace(c.Query("q"))), domain: strings.ToLower(strings.TrimSpace(c.Query("domain"))),
		tag: strings.ToLower(strings.TrimSpace(c.Query("tag"))), group: strings.TrimSpace(c.Query("group")),
		providers: csvSet(c.Query("provider")), statuses: csvSet(c.Query("status")),
		egress: strings.TrimSpace(c.Query("egress")),
	}
	if v, err := strconv.ParseUint(c.Query("exclude_group"), 10, 64); err == nil {
		f.exclude = uint(v)
	}
	return f
}

func (f credFilter) match(a *coreauth.Auth, status string, groups []uint, excluded map[string]bool) bool {
	if len(f.providers) > 0 && !f.providers[strings.ToLower(a.Provider)] {
		return false
	}
	if len(f.statuses) > 0 && !f.statuses[status] {
		return false
	}
	if f.egress != "" && strings.TrimSpace(stringFromAny(a.Metadata["egress_id"])) != f.egress {
		return false
	}
	email := cluster.CredentialEmail(a)
	if f.domain != "" && cluster.EmailDomain(email) != f.domain {
		return false
	}
	if f.tag != "" {
		ok := false
		for _, t := range cluster.CredentialTags(a) {
			ok = ok || t == f.tag
		}
		if !ok {
			return false
		}
	}
	switch f.group {
	case "":
	case "none":
		if len(groups) > 0 {
			return false
		}
	default:
		gid, _ := strconv.ParseUint(f.group, 10, 64)
		ok := false
		for _, g := range groups {
			ok = ok || uint64(g) == gid
		}
		if !ok {
			return false
		}
	}
	if excluded != nil && excluded[a.ID] {
		return false
	}
	if f.q != "" {
		hay := strings.ToLower(strings.Join([]string{a.ID, a.Label, email, noteFromAuth(a), prefixFromAuth(a), authFileName(a)}, " "))
		if !strings.Contains(hay, f.q) {
			return false
		}
	}
	return true
}

// SearchCredentials: GET /credentials/search
func (h *Handler) SearchCredentials(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	snap, err := h.repo.Scope(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "scope_load_failed", err)
		return
	}
	f := credFilterFromQuery(c)
	groupsOf := snap.GroupsOfAuth()
	var excluded map[string]bool
	if f.exclude != 0 {
		excluded = map[string]bool{}
		for _, a := range snap.Members(f.exclude) {
			excluded[a] = true
		}
	}
	now := time.Now()
	facets := map[string]map[string]int{"provider": {}, "status": {}, "domain": {}, "tag": {}}
	var matched []*coreauth.Auth
	statusOf := map[string]string{}
	all := h.liveAuths(ctx)
	seqs, _ := h.repo.AuthSeqs(ctx)
	seqQ, seqOnly := parseSeqQuery(f.q)
	for _, a := range all {
		st := credentialStatus(a, now)
		statusOf[a.ID] = st
		facets["provider"][a.Provider]++
		facets["status"][st]++
		if d := cluster.EmailDomain(cluster.CredentialEmail(a)); d != "" {
			facets["domain"][d]++
		}
		for _, t := range cluster.CredentialTags(a) {
			facets["tag"][t]++
		}
		fa := f
		if seqQ > 0 && seqs[a.ID] == seqQ {
			fa.q = "" // "#N" / "N" hits the sequence number exactly
		} else if seqOnly {
			continue
		}
		if fa.match(a, st, groupsOf[a.ID], excluded) {
			matched = append(matched, a)
		}
	}
	if c.Query("ids_only") == "1" || c.Query("ids_only") == "true" {
		ids := make([]string, 0, len(matched))
		for _, a := range matched {
			ids = append(ids, a.ID)
		}
		c.JSON(http.StatusOK, gin.H{"ids": ids, "total": len(ids)})
		return
	}
	sortKey, desc := credentialSortParams(c)
	stats := h.credentialSortStats(ctx, all)
	window, windowDur := callStatWindow(c.Query("stats_window"))
	calls := h.credentialCallStats(ctx, window, windowDur)
	sortCredentials(matched, sortKey, desc, statusOf, stats, calls, seqs)
	page, size := pageParams(c, 50, 500)
	start := (page - 1) * size
	items := make([]gin.H, 0, size)
	var capUsage map[string]home.RequestCapUsage
	if h.runtime != nil && start < len(matched) {
		capUsage = h.runtime.RequestCapUsage(ctx, matched[start:min(start+size, len(matched))])
	}
	for i := start; i < len(matched) && i < start+size; i++ {
		a := matched[i]
		e := authFileEntry(a)
		if u, ok := capUsage[a.ID]; ok {
			e["request_cap"] = u
		}
		if t, ok := stats.LastUsed[a.ID]; ok {
			e["last_used_at"] = t
		}
		if cs := calls[a.ID]; cs != nil {
			e["call_stats"] = cs
		}
		e["tags"] = cluster.CredentialTags(a)
		e["group_ids"] = nonNilUints(groupsOf[a.ID])
		e["state"] = statusOf[a.ID]
		if n := seqs[a.ID]; n > 0 {
			e["seq"] = n
		}
		items = append(items, e)
	}
	h.annotateQuotaProbes(c.Request.Context(), items)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(matched), "all": len(all), "page": page, "page_size": size, "facets": facets, "sort": sortKey, "order": map[bool]string{true: "desc", false: "asc"}[desc], "stats_window": window})
}

func nonNilUints(v []uint) []uint {
	if v == nil {
		return []uint{}
	}
	return v
}

// BulkCredentials: POST /credentials/bulk
func (h *Handler) BulkCredentials(c *gin.Context) {
	var req struct {
		IDs     []string `json:"ids"`
		Action  string   `json:"action"`
		Tags    []string `json:"tags"`
		GroupID uint     `json:"group_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	if len(req.IDs) > 5000 {
		respondError(c, http.StatusBadRequest, "too many ids (max 5000)", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	switch req.Action {
	case "add_to_group", "remove_from_group":
		if req.GroupID == 0 {
			respondError(c, http.StatusBadRequest, "group_id is required", nil)
			return
		}
		var res cluster.BulkResult
		var err error
		if req.Action == "add_to_group" {
			res, err = h.repo.BulkChannelGroupMembers(ctx, req.GroupID, req.IDs, nil)
		} else {
			res, err = h.repo.BulkChannelGroupMembers(ctx, req.GroupID, nil, req.IDs)
		}
		if err != nil {
			respondError(c, http.StatusBadRequest, "bulk_failed", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "result": res})
		return
	case "enable", "disable", "add_tags", "remove_tags", "set_tags":
	default:
		respondError(c, http.StatusBadRequest, "unknown action", nil)
		return
	}
	tags := cluster.NormalizeTags(req.Tags)
	if strings.HasSuffix(req.Action, "_tags") && req.Action != "set_tags" && len(tags) == 0 {
		respondError(c, http.StatusBadRequest, "tags are required", nil)
		return
	}
	updated, unchanged := 0, 0
	var failed []string
	for _, id := range req.IDs {
		auth, err := h.findOAuthAuth(ctx, authIdentifier{ID: id, Name: id})
		if err != nil || auth == nil {
			failed = append(failed, id)
			continue
		}
		changed := false
		switch req.Action {
		case "enable", "disable":
			want := req.Action == "disable"
			if auth.Disabled != want {
				applyAuthDisabledStatus(auth, want)
				changed = true
			}
		default:
			cur := cluster.CredentialTags(auth)
			set := map[string]bool{}
			if req.Action != "set_tags" {
				for _, t := range cur {
					set[t] = true
				}
			}
			for _, t := range tags {
				set[t] = req.Action != "remove_tags"
			}
			var next []string
			for t, keep := range set {
				if keep {
					next = append(next, t)
				}
			}
			next = cluster.NormalizeTags(next)
			if strings.Join(next, ",") != strings.Join(cur, ",") {
				if auth.Metadata == nil {
					auth.Metadata = map[string]any{}
				}
				if len(next) == 0 {
					delete(auth.Metadata, "tags")
				} else {
					anyTags := make([]any, 0, len(next))
					for _, t := range next {
						anyTags = append(anyTags, t)
					}
					auth.Metadata["tags"] = anyTags
				}
				auth.UpdatedAt = time.Now().UTC()
				changed = true
			}
		}
		if !changed {
			unchanged++
			continue
		}
		if _, err := h.repo.UpsertAuth(ctx, auth, "update"); err != nil {
			failed = append(failed, id)
			continue
		}
		updated++
	}
	if updated > 0 {
		if err := h.refreshAuths(ctx); err != nil {
			respondError(c, http.StatusInternalServerError, "reload_failed", err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "updated": updated, "unchanged": unchanged, "failed": nonNilStrings(failed)})
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// GroupsSummary: GET /groups/summary — server-side availability per group.
func (h *Handler) GroupsSummary(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	snap, err := h.repo.Scope(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "scope_load_failed", err)
		return
	}
	now := time.Now()
	status := map[string]string{}
	for _, a := range h.liveAuths(ctx) {
		status[a.ID] = credentialStatus(a, now)
	}
	byCh, byMg, unrestricted := snap.KeysBoundTo()
	cgs := make([]gin.H, 0, len(snap.ChannelGroups))
	grouped := map[string]bool{}
	for _, id := range sortedGroupIDs(snap.ChannelGroups) {
		g := snap.ChannelGroups[id]
		counts := map[string]int{credStatusOK: 0, credStatusCooling: 0, credStatusError: 0, credStatusDisabled: 0, "missing": 0}
		members := snap.Members(id)
		for _, m := range members {
			grouped[m] = true
			if st, ok := status[m]; ok {
				counts[st]++
			} else {
				counts["missing"]++
			}
		}
		rule, hasRule := snap.Rules[id]
		cgs = append(cgs, gin.H{
			"id": id, "channel_name": g.ChannelName, "enabled": !g.Disabled,
			"static_count": len(snap.StaticMembers[id]), "rule_count": len(snap.RuleMembers[id]), "total": len(members),
			"usable": counts[credStatusOK] + counts[credStatusCooling], "ok": counts[credStatusOK], "cooling": counts[credStatusCooling],
			"error": counts[credStatusError], "disabled": counts[credStatusDisabled], "missing": counts["missing"],
			"has_rule": hasRule && rule.Enabled, "keys": byCh[id],
		})
	}
	mgs := make([]gin.H, 0, len(snap.ModelGroups))
	for _, id := range sortedGroupIDs(snap.ModelGroups) {
		g := snap.ModelGroups[id]
		mgs = append(mgs, gin.H{"id": id, "group_name": g.GroupName, "enabled": !g.Disabled, "models": len(snap.ModelGroupModels(id)), "keys": byMg[id]})
	}
	ungrouped := 0
	for id := range status {
		if !grouped[id] {
			ungrouped++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"credential_groups": cgs, "model_groups": mgs,
		"credentials": len(status), "ungrouped_credentials": ungrouped,
		"keys": snap.KeyCount, "unrestricted_keys": unrestricted,
		"snapshot": gin.H{"built_at": snap.BuiltAt, "build_ms": float64(snap.BuildDuration.Microseconds()) / 1000},
	})
}

func sortedGroupIDs[V any](m map[uint]V) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ChannelGroupMembers: GET /channel-groups/:id/members (paged, searchable).
func (h *Handler) ChannelGroupMembers(c *gin.Context) {
	id, ok := channelIDFromParam(c)
	if !ok {
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	snap, err := h.repo.Scope(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "scope_load_failed", err)
		return
	}
	if _, ok := snap.ChannelGroups[id]; !ok {
		respondError(c, http.StatusNotFound, "not_found", nil)
		return
	}
	static, rule := map[string]bool{}, map[string]bool{}
	for _, m := range snap.StaticMembers[id] {
		static[m] = true
	}
	for _, m := range snap.RuleMembers[id] {
		rule[m] = true
	}
	byID := map[string]*coreauth.Auth{}
	for _, a := range h.liveAuths(ctx) {
		byID[a.ID] = a
	}
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	source := c.Query("source")
	now := time.Now()
	type row struct {
		ID, Label, Email, Provider, State, Source string
		Missing                                   bool
	}
	var rows []row
	for _, m := range snap.Members(id) {
		r := row{ID: m}
		switch {
		case static[m] && rule[m]:
			r.Source = "both"
		case rule[m]:
			r.Source = "rule"
		default:
			r.Source = "static"
		}
		if source != "" && r.Source != source && r.Source != "both" {
			continue
		}
		if a := byID[m]; a != nil {
			r.Label, r.Email, r.Provider, r.State = a.Label, cluster.CredentialEmail(a), a.Provider, credentialStatus(a, now)
		} else {
			r.Missing, r.State = true, "missing"
		}
		if q != "" && !strings.Contains(strings.ToLower(r.ID+" "+r.Label+" "+r.Email), q) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Label+rows[i].ID) < strings.ToLower(rows[j].Label+rows[j].ID)
	})
	if c.Query("ids_only") == "1" {
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.Source != "rule" { // only static members can be removed
				ids = append(ids, r.ID)
			}
		}
		c.JSON(http.StatusOK, gin.H{"ids": ids, "total": len(ids)})
		return
	}
	page, size := pageParams(c, 50, 500)
	items := make([]gin.H, 0, size)
	for i := (page - 1) * size; i < len(rows) && i < page*size; i++ {
		r := rows[i]
		items = append(items, gin.H{"id": r.ID, "label": r.Label, "email": r.Email, "provider": r.Provider, "state": r.State, "source": r.Source, "missing": r.Missing})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(rows), "page": page, "page_size": size,
		"static_count": len(snap.StaticMembers[id]), "rule_count": len(snap.RuleMembers[id])})
}

// BulkChannelGroupMembers: POST /channel-groups/:id/members/bulk {add, remove}
func (h *Handler) BulkChannelGroupMembers(c *gin.Context) {
	id, ok := channelIDFromParam(c)
	if !ok {
		return
	}
	var req struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	res, err := h.repo.BulkChannelGroupMembers(ctx, id, req.Add, req.Remove)
	if err != nil {
		respondError(c, http.StatusBadRequest, "bulk_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "result": res})
}

// BulkModelGroupMembers: POST /model-groups/:id/members/bulk {add, remove, channels}
func (h *Handler) BulkModelGroupMembers(c *gin.Context) {
	id, ok := channelIDFromParam(c)
	if !ok {
		return
	}
	var req struct {
		Add      []string `json:"add"`
		Remove   []string `json:"remove"`
		Channels []uint   `json:"channels"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	res, err := h.repo.BulkModelGroupMembers(ctx, id, req.Add, req.Remove, req.Channels)
	if err != nil {
		respondError(c, http.StatusBadRequest, "bulk_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "result": res})
}

// GetChannelGroupRule: GET /channel-groups/:id/rule
func (h *Handler) GetChannelGroupRule(c *gin.Context) {
	id, ok := channelIDFromParam(c)
	if !ok {
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	snap, err := h.repo.Scope(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "scope_load_failed", err)
		return
	}
	rec, has := snap.Rules[id]
	if !has {
		c.JSON(http.StatusOK, gin.H{"rule": nil, "enabled": false, "matched": 0})
		return
	}
	rule, _ := rec.ParsedRule()
	c.JSON(http.StatusOK, gin.H{"rule": rule, "enabled": rec.Enabled, "matched": len(snap.RuleMembers[id]), "updated_at": rec.UpdatedAt})
}

// PutChannelGroupRule: PUT /channel-groups/:id/rule {rule, enabled}; DELETE removes it.
func (h *Handler) PutChannelGroupRule(c *gin.Context) {
	id, ok := channelIDFromParam(c)
	if !ok {
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	if c.Request.Method == http.MethodDelete {
		if err := h.repo.PutChannelGroupRule(ctx, id, nil, false); err != nil {
			respondError(c, http.StatusInternalServerError, "rule_failed", err)
			return
		}
		cluster.InvalidateScopeCaches()
		respondOK(c)
		return
	}
	var req struct {
		Rule    cluster.GroupRule `json:"rule"`
		Enabled *bool             `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	if req.Rule.Empty() && !req.Rule.MatchAll {
		respondError(c, http.StatusBadRequest, "rule needs at least one condition", nil)
		return
	}
	if err := h.repo.PutChannelGroupRule(ctx, id, &req.Rule, enabled); err != nil {
		respondError(c, http.StatusBadRequest, "rule_failed", err)
		return
	}
	cluster.InvalidateScopeCaches()
	snap, err := h.repo.Scope(ctx)
	matched := 0
	if err == nil {
		matched = len(snap.RuleMembers[id])
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "matched": matched})
}

// PreviewGroupRule: POST /group-rules/preview {rule}
func (h *Handler) PreviewGroupRule(c *gin.Context) {
	var req struct {
		Rule cluster.GroupRule `json:"rule"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	if err := req.Rule.Validate(); err != nil {
		respondError(c, http.StatusBadRequest, "invalid rule", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	matched := 0
	sample := make([]gin.H, 0, 20)
	for _, a := range h.liveAuths(ctx) {
		if req.Rule.Matches(a) {
			matched++
			if len(sample) < 20 {
				sample = append(sample, gin.H{"id": a.ID, "label": a.Label, "provider": a.Provider})
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"matched": matched, "sample": sample, "empty": req.Rule.Empty() && !req.Rule.MatchAll})
}

// SearchAPIKeys: GET /api-keys/search?q&channel&model_group&scope&page&page_size&ids_only
func (h *Handler) SearchAPIKeys(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	entries, _, err := h.repo.ListAPIKeyEntriesWithETag(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "api_keys_load_failed", err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	ch, _ := strconv.ParseUint(c.Query("channel"), 10, 64)
	mg, _ := strconv.ParseUint(c.Query("model_group"), 10, 64)
	scope := c.Query("scope")
	has := func(list []uint, v uint64) bool {
		for _, x := range list {
			if uint64(x) == v {
				return true
			}
		}
		return false
	}
	var matched []cluster.APIKeyEntry
	for _, e := range entries {
		name := ""
		if e.DisplayName != nil {
			name = *e.DisplayName
		}
		if q != "" && !strings.Contains(strings.ToLower(name+" "+e.APIKey), q) {
			continue
		}
		if ch != 0 && !has(e.Channels, ch) {
			continue
		}
		if mg != 0 && !has(e.ModelGroups, mg) {
			continue
		}
		restricted := len(e.Channels) > 0 || len(e.ModelGroups) > 0
		if (scope == "unrestricted" && restricted) || (scope == "restricted" && !restricted) {
			continue
		}
		matched = append(matched, e)
	}
	if c.Query("ids_only") == "1" {
		ids := make([]uint, 0, len(matched))
		for _, e := range matched {
			ids = append(ids, e.ID)
		}
		c.JSON(http.StatusOK, gin.H{"ids": ids, "total": len(ids)})
		return
	}
	page, size := pageParams(c, 50, 500)
	lo, hi := (page-1)*size, page*size
	if lo > len(matched) {
		lo = len(matched)
	}
	if hi > len(matched) {
		hi = len(matched)
	}
	resp := apiKeyEntriesResponse(matched[lo:hi])
	c.JSON(http.StatusOK, gin.H{"items": resp["items"], "total": len(matched), "all": len(entries), "page": page, "page_size": size})
}

// BulkAPIKeys: POST /api-keys/bulk (KeyScopeChange)
func (h *Handler) BulkAPIKeys(c *gin.Context) {
	var req cluster.KeyScopeChange
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	res, err := h.repo.BulkUpdateAPIKeyScopes(ctx, req)
	if err != nil {
		respondError(c, http.StatusBadRequest, "bulk_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "updated": res.Added, "unchanged": res.Unchanged})
}

// ExportGroups: GET /groups/export?format=json|csv
func (h *Handler) ExportGroups(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	doc, err := h.repo.ExportGroups(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "export_failed", err)
		return
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	if c.Query("format") == "csv" {
		label := map[string]string{}
		for _, a := range h.liveAuths(ctx) {
			label[a.ID] = cluster.CredentialEmail(a)
		}
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"type", "group", "member", "label", "credential_groups"})
		for _, g := range doc.CredentialGroups {
			for _, m := range g.Members {
				_ = w.Write([]string{"credential", g.Name, m, label[m], ""})
			}
		}
		for _, g := range doc.ModelGroups {
			for _, m := range g.Models {
				_ = w.Write([]string{"model", g.Name, m.Model, "", strings.Join(m.CredentialGroups, ";")})
			}
		}
		w.Flush()
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="groups-%s.csv"`, stamp))
		c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="groups-%s.json"`, stamp))
	c.JSON(http.StatusOK, doc)
}

// ParseGroupCSV converts the CSV export format into a document (rules are JSON-only).
func ParseGroupCSV(r io.Reader) (cluster.GroupExport, error) {
	doc := cluster.GroupExport{Version: 1}
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return doc, err
	}
	cg := map[string]*cluster.CredentialGroupExport{}
	mg := map[string]*cluster.ModelGroupExport{}
	var cgOrder, mgOrder []string
	for i, row := range rows {
		if len(row) < 3 || (i == 0 && strings.EqualFold(strings.TrimSpace(row[0]), "type")) {
			continue
		}
		typ, group, member := strings.ToLower(strings.TrimSpace(row[0])), strings.TrimSpace(row[1]), strings.TrimSpace(row[2])
		if group == "" || member == "" {
			continue
		}
		switch typ {
		case "credential", "cred":
			if cg[group] == nil {
				cg[group] = &cluster.CredentialGroupExport{Name: group, Enabled: true}
				cgOrder = append(cgOrder, group)
			}
			cg[group].Members = append(cg[group].Members, member)
		case "model":
			if mg[group] == nil {
				mg[group] = &cluster.ModelGroupExport{Name: group, Enabled: true}
				mgOrder = append(mgOrder, group)
			}
			e := cluster.ModelEntryExport{Model: member}
			if len(row) > 4 && strings.TrimSpace(row[4]) != "" {
				for _, n := range strings.Split(row[4], ";") {
					if n = strings.TrimSpace(n); n != "" {
						e.CredentialGroups = append(e.CredentialGroups, n)
					}
				}
			}
			mg[group].Models = append(mg[group].Models, e)
		default:
			return doc, fmt.Errorf("line %d: unknown type %q (credential|model)", i+1, row[0])
		}
	}
	for _, n := range cgOrder {
		doc.CredentialGroups = append(doc.CredentialGroups, *cg[n])
	}
	for _, n := range mgOrder {
		doc.ModelGroups = append(doc.ModelGroups, *mg[n])
	}
	return doc, nil
}

// ImportGroups: POST /groups/import {format, data, mode: merge|replace, dry_run}
func (h *Handler) ImportGroups(c *gin.Context) {
	var req struct {
		Format string `json:"format"`
		Data   string `json:"data"`
		Mode   string `json:"mode"`
		DryRun bool   `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	if len(req.Data) > 8<<20 {
		respondError(c, http.StatusBadRequest, "document too large (max 8MB)", nil)
		return
	}
	var doc cluster.GroupExport
	var err error
	if strings.EqualFold(req.Format, "csv") {
		doc, err = ParseGroupCSV(strings.NewReader(req.Data))
	} else {
		err = json.Unmarshal([]byte(req.Data), &doc)
	}
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid document", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	byKey := map[string]string{}
	for _, a := range h.liveAuths(ctx) {
		byKey[strings.ToLower(a.ID)] = a.ID
		if e := strings.ToLower(cluster.CredentialEmail(a)); e != "" {
			if _, dup := byKey[e]; !dup {
				byKey[e] = a.ID
			}
		}
		if l := strings.ToLower(strings.TrimSpace(a.Label)); l != "" {
			if _, dup := byKey[l]; !dup {
				byKey[l] = a.ID
			}
		}
	}
	resolve := func(s string) (string, bool) {
		id, ok := byKey[strings.ToLower(strings.TrimSpace(s))]
		return id, ok
	}
	rep, err := h.repo.ImportGroups(ctx, doc, resolve, req.Mode == "replace", req.DryRun)
	if err != nil {
		respondError(c, http.StatusBadRequest, "import_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "report": rep})
}

// coolingModelCount counts models whose cooldown is still active.
func coolingModelCount(a *coreauth.Auth, now time.Time) int {
	n := 0
	for _, e := range modelCooldownEntries(a, now) {
		if c, _ := e["cooling"].(bool); c {
			n++
		}
	}
	return n
}

// parseSeqQuery recognizes "#N" (sequence only) and bare "N" (sequence or text).
func parseSeqQuery(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	only := strings.HasPrefix(q, "#")
	n, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, only
}
