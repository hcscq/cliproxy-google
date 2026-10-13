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

// EgressMove is one planned credential move.
type EgressMove struct {
	CredentialID string `json:"credential_id"`
	Label        string `json:"label"`
	From         string `json:"from"`
	FromIP       string `json:"from_ip"`
	To           string `json:"to"`
	ToIP         string `json:"to_ip"`
}

// planEgressRebalance evens credential counts over healthy egresses with the fewest
// moves. Heavier egresses keep the extra slots; within an egress the most recently
// created credentials move first (older ones keep their long-standing IP).
func planEgressRebalance(items []home.EgressStatus, auths []*coreauth.Auth) []EgressMove {
	var healthy []home.EgressStatus
	byURL := map[string]home.EgressStatus{}
	for _, it := range items {
		if it.Healthy && !it.Removed {
			healthy = append(healthy, it)
			byURL[it.ProxyURL] = it
		}
	}
	if len(healthy) < 2 {
		return nil
	}
	members := map[string][]*coreauth.Auth{}
	total := 0
	for _, a := range auths {
		if _, ok := byURL[strings.TrimSpace(a.ProxyURL)]; ok && strings.EqualFold(a.Provider, "antigravity") {
			members[a.ProxyURL] = append(members[a.ProxyURL], a)
			total++
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		ci, cj := len(members[healthy[i].ProxyURL]), len(members[healthy[j].ProxyURL])
		if ci != cj {
			return ci > cj
		}
		return healthy[i].Name < healthy[j].Name
	})
	base, extra := total/len(healthy), total%len(healthy)
	capOf := map[string]int{}
	for i, h := range healthy {
		capOf[h.ProxyURL] = base
		if i < extra {
			capOf[h.ProxyURL]++
		}
	}
	var surplus []*coreauth.Auth
	for _, h := range healthy {
		list := members[h.ProxyURL]
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
				return list[i].CreatedAt.After(list[j].CreatedAt)
			}
			return list[i].ID < list[j].ID
		})
		if over := len(list) - capOf[h.ProxyURL]; over > 0 {
			surplus = append(surplus, list[:over]...)
		}
	}
	var moves []EgressMove
	for _, h := range healthy {
		for need := capOf[h.ProxyURL] - len(members[h.ProxyURL]); need > 0 && len(surplus) > 0; need-- {
			a := surplus[0]
			surplus = surplus[1:]
			from := byURL[a.ProxyURL]
			moves = append(moves, EgressMove{CredentialID: a.ID, Label: a.Label, From: from.Name, FromIP: from.IP, To: h.Name, ToIP: h.IP})
		}
	}
	return moves
}

// RebalanceEgress: POST /proxy/egress/rebalance {dry_run} — explicit, previewable
// rebalance onto new/less-loaded egresses (never automatic: IP stability first).
func (h *Handler) RebalanceEgress(c *gin.Context) {
	var body struct {
		DryRun *bool `json:"dry_run"`
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
	moves := planEgressRebalance(items, auths)
	if dry || len(moves) == 0 {
		c.JSON(http.StatusOK, gin.H{"dry_run": dry, "moves": nonNilMoves(moves)})
		return
	}
	specs := map[string]home.EgressSpec{}
	for _, it := range items {
		specs[it.Name] = it.EgressSpec
	}
	applied := 0
	for _, m := range moves {
		a, _, errGet := h.repo.GetAuth(ctx, m.CredentialID)
		if errGet != nil || a == nil {
			continue
		}
		home.ApplyEgressMetadata(a, specs[m.To])
		a.UpdatedAt = time.Now().UTC()
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

func nonNilMoves(m []EgressMove) []EgressMove {
	if m == nil {
		return []EgressMove{}
	}
	return m
}

// AckEgress: POST /proxy/egress/ack {id} accepts a changed public IP.
func (h *Handler) AckEgress(c *gin.Context) {
	h.egressRegistryAction(c, func(ctx *gin.Context, id string) error { return h.runtime.AckEgressPublicIP(ctx.Request.Context(), id) })
}

// ForgetEgress: POST /proxy/egress/forget {id} drops a removed egress with no credentials.
func (h *Handler) ForgetEgress(c *gin.Context) {
	h.egressRegistryAction(c, func(ctx *gin.Context, id string) error {
		items, _ := h.runtime.EgressStatuses(ctx.Request.Context())
		for _, it := range items {
			if it.Name == id && it.Credentials > 0 {
				return errEgressInUse
			}
		}
		return h.runtime.ForgetEgress(ctx.Request.Context(), id)
	})
}

type egressError string

func (e egressError) Error() string { return string(e) }

const errEgressInUse = egressError("egress still has credentials; move them first (设置出口)")

func (h *Handler) egressRegistryAction(c *gin.Context, fn func(*gin.Context, string) error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.ID) == "" || h.runtime == nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	if err := fn(c, strings.TrimSpace(body.ID)); err != nil {
		respondError(c, http.StatusBadRequest, "egress_action_failed", err)
		return
	}
	respondOK(c)
}
