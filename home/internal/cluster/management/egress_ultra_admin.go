package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

// UltraEgressCred is one Ultra credential in the report.
type UltraEgressCred struct {
	ID      string    `json:"id"`
	Seq     int64     `json:"seq,omitempty"`
	Label   string    `json:"label"`
	Created time.Time `json:"created_at"`
	Note    string    `json:"note,omitempty"`
}

// UltraEgressGroup is one egress with more Ultra credentials than allowed.
type UltraEgressGroup struct {
	Egress      string            `json:"egress"`
	IP          string            `json:"ip"`
	PublicIP    string            `json:"public_ip,omitempty"`
	Count       int               `json:"count"`
	Credentials []UltraEgressCred `json:"credentials"`
}

func authIsUltra(a *coreauth.Auth) bool {
	if a == nil {
		return false
	}
	t, _ := a.Metadata["subscription_tier"].(string)
	return strings.EqualFold(a.Provider, "antigravity") && strings.Contains(strings.ToLower(t), "ultra")
}

// ultraByEgress groups Ultra credentials by egress URL, oldest first.
func ultraByEgress(auths []*coreauth.Auth) map[string][]*coreauth.Auth {
	out := map[string][]*coreauth.Auth{}
	for _, a := range auths {
		if p := strings.TrimSpace(a.ProxyURL); p != "" && authIsUltra(a) {
			out[p] = append(out[p], a)
		}
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
				return list[i].CreatedAt.Before(list[j].CreatedAt)
			}
			return list[i].ID < list[j].ID
		})
	}
	return out
}

// planUltraRebalance moves the newest Ultra credentials off egresses over the
// limit onto healthy, assignable egresses with room (fewest Ultra, then fewest
// credentials). The oldest `limit` keep their IP. Surplus with no target stays.
func planUltraRebalance(items []home.EgressStatus, auths []*coreauth.Auth, limit, maxPer int, allowPrimary bool) []EgressMove {
	if limit <= 0 {
		return nil
	}
	ultra := ultraByEgress(auths)
	total := map[string]int{}
	for _, a := range auths {
		if strings.EqualFold(a.Provider, "antigravity") && a.ProxyURL != "" {
			total[strings.TrimSpace(a.ProxyURL)]++
		}
	}
	byURL := map[string]home.EgressStatus{}
	for _, it := range items {
		byURL[it.ProxyURL] = it
	}
	uc := map[string]int{}
	for u, l := range ultra {
		uc[u] = len(l)
	}
	urls := make([]string, 0, len(ultra))
	for u := range ultra {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	var moves []EgressMove
	for _, u := range urls {
		list := ultra[u]
		from, managed := byURL[u]
		if !managed {
			continue // manual proxy: not ours to move
		}
		for i := len(list) - 1; i >= limit; i-- {
			best := -1
			for j, it := range items {
				if it.ProxyURL == u || !it.Healthy || it.Removed || (it.Primary && !allowPrimary) {
					continue
				}
				if uc[it.ProxyURL] >= limit || (maxPer > 0 && total[it.ProxyURL] >= maxPer) {
					continue
				}
				if best < 0 {
					best = j
					continue
				}
				b := items[best].ProxyURL
				if uc[it.ProxyURL] < uc[b] || (uc[it.ProxyURL] == uc[b] && (total[it.ProxyURL] < total[b] || (total[it.ProxyURL] == total[b] && it.Name < items[best].Name))) {
					best = j
				}
			}
			if best < 0 {
				break
			}
			to := items[best]
			a := list[i]
			uc[u]--
			uc[to.ProxyURL]++
			total[u]--
			total[to.ProxyURL]++
			moves = append(moves, EgressMove{CredentialID: a.ID, Label: a.Label, From: from.Name, FromIP: from.IP, To: to.Name, ToIP: to.IP})
		}
	}
	return moves
}

// UltraEgress: GET /proxy/egress/ultra — Ultra-per-IP violations and pending notes.
func (h *Handler) UltraEgress(c *gin.Context) {
	if h.runtime == nil {
		respondError(c, http.StatusServiceUnavailable, "egress_unavailable", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	items, _ := h.runtime.EgressStatuses(ctx)
	auths, err := h.repo.ListAuths(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", err)
		return
	}
	settings := h.runtime.EgressSettings(ctx)
	limit := settings.UltraLimit()
	seqs, _ := h.repo.AuthSeqs(ctx)
	byURL := map[string]home.EgressStatus{}
	for _, it := range items {
		byURL[it.ProxyURL] = it
	}
	groups := []UltraEgressGroup{}
	ultraTotal := 0
	for u, list := range ultraByEgress(auths) {
		ultraTotal += len(list)
		if limit <= 0 || len(list) <= limit {
			continue
		}
		g := UltraEgressGroup{Egress: byURL[u].Name, IP: byURL[u].IP, PublicIP: byURL[u].PublicIP, Count: len(list)}
		if g.Egress == "" {
			g.Egress = u
		}
		for _, a := range list {
			note, _ := a.Metadata[home.EgressUltraNoteKey].(string)
			g.Credentials = append(g.Credentials, UltraEgressCred{ID: a.ID, Seq: seqs[a.ID], Label: a.Label, Created: a.CreatedAt, Note: note})
		}
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Egress < groups[j].Egress
	})
	c.JSON(http.StatusOK, gin.H{"limit": limit, "ultra_total": ultraTotal, "violations": groups})
}

// RebalanceUltraEgress: POST /proxy/egress/ultra/rebalance {dry_run, allow_primary} —
// explicit, previewable: spreads Ultra credentials so no egress exceeds the limit.
func (h *Handler) RebalanceUltraEgress(c *gin.Context) {
	var body struct {
		DryRun       *bool `json:"dry_run"`
		AllowPrimary *bool `json:"allow_primary"`
	}
	_ = c.ShouldBindJSON(&body)
	dry := body.DryRun == nil || *body.DryRun
	if h.runtime == nil {
		respondError(c, http.StatusServiceUnavailable, "egress_unavailable", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	items, err := h.runtime.EgressStatuses(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "egress_status_failed", err)
		return
	}
	auths, err := h.repo.ListAuths(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "auth_load_failed", err)
		return
	}
	settings := h.runtime.EgressSettings(ctx)
	allow := settings.AllowPrimary
	if body.AllowPrimary != nil {
		allow = *body.AllowPrimary
	}
	moves := planUltraRebalance(assignableEgressStatuses(items, settings), auths, settings.UltraLimit(), settings.MaxPerEgress, allow)
	if seqs, err := h.repo.AuthSeqs(ctx); err == nil {
		for i := range moves {
			moves[i].Seq = seqs[moves[i].CredentialID]
		}
	}
	if dry || len(moves) == 0 {
		c.JSON(http.StatusOK, gin.H{"dry_run": dry, "moves": nonNilMoves(moves)})
		return
	}
	specs := map[string]home.EgressSpec{}
	for _, it := range items {
		specs[it.Name] = it.EgressSpec
	}
	now := time.Now().UTC()
	applied := 0
	for _, m := range moves {
		a, _, errGet := h.repo.GetAuth(ctx, m.CredentialID)
		if errGet != nil || a == nil {
			continue
		}
		home.ApplyEgressMetadata(a, specs[m.To])
		a.Metadata[home.EgressUltraPlacedKey] = now.Format(time.RFC3339)
		delete(a.Metadata, home.EgressUltraNoteKey)
		a.UpdatedAt = now
		if _, errUp := h.repo.UpsertAuth(ctx, a, "upsert"); errUp == nil {
			applied++
		}
	}
	if errRefresh := h.refreshAuths(ctx); errRefresh != nil {
		respondError(c, http.StatusInternalServerError, "reload_failed", errRefresh)
		return
	}
	c.JSON(http.StatusOK, gin.H{"dry_run": false, "moves": moves, "applied": applied})
}
