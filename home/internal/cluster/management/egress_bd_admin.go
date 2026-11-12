package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

// GetBDEgress: GET /proxy/egress/bd — inventory + non-secret config (has_password only).
func (h *Handler) GetBDEgress(c *gin.Context) {
	if h == nil || h.runtime == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "has_password": false, "items": []any{}})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	view, rows, err := h.runtime.BDSettings(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "bd_egress_load_failed", err)
		return
	}
	// Defence: never put a password in the response, even if a future field is added.
	payload := gin.H{
		"enabled":           view.Enabled,
		"endpoint":          view.Endpoint,
		"base_username":     view.BaseUsername,
		"username_template": view.UsernameTemplate,
		"zone":              view.Zone,
		"relay_port":        view.RelayPort,
		"relay_service":     view.RelayService,
		"probe_url":         view.ProbeURL,
		"has_password":      view.HasPassword,
		"items":             bdItemsForPanel(rows),
	}
	c.JSON(http.StatusOK, payload)
}

func bdItemsForPanel(rows []home.BDEgressRow) []gin.H {
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id":              r.ID,
			"expected_ip":     r.ExpectedIP,
			"enabled":         r.Enabled,
			"draining":        r.Draining,
			"max_credentials": r.EffectiveMax(),
			"chain_ref":       r.ChainRef,
		})
	}
	return out
}

// PutBDEgress: PUT /proxy/egress/bd — upsert one inventory row (non-secret fields only).
// Password fields in the body are ignored and never stored.
func (h *Handler) PutBDEgress(c *gin.Context) {
	if h == nil || h.runtime == nil {
		respondError(c, http.StatusServiceUnavailable, "egress_unavailable", nil)
		return
	}
	var body struct {
		ExpectedIP     string `json:"expected_ip"`
		Enabled        *bool  `json:"enabled"`
		Draining       *bool  `json:"draining"`
		MaxCredentials *int   `json:"max_credentials"`
		ChainRef       string `json:"chain_ref"`
		// Secret-looking fields are accepted only so we can drop them: never stored.
		Password         string `json:"password"`
		BDPassword       string `json:"bd_password"`
		ManagementAPIKey string `json:"management_api_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	// Explicitly discard any secret the client sent.
	body.Password, body.BDPassword, body.ManagementAPIKey = "", "", ""
	row := home.BDEgressRow{
		ExpectedIP: strings.TrimSpace(body.ExpectedIP),
		Enabled:    true,
		ChainRef:   body.ChainRef,
	}
	if body.Enabled != nil {
		row.Enabled = *body.Enabled
	}
	if body.Draining != nil {
		row.Draining = *body.Draining
	}
	if body.MaxCredentials != nil {
		row.MaxCredentials = *body.MaxCredentials
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	saved, err := h.runtime.UpsertBDEgress(ctx, row)
	if err != nil {
		respondError(c, http.StatusBadRequest, "bd_egress_save_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":              saved.ID,
		"expected_ip":     saved.ExpectedIP,
		"enabled":         saved.Enabled,
		"draining":        saved.Draining,
		"max_credentials": saved.EffectiveMax(),
		"chain_ref":       saved.ChainRef,
	})
}

// DeleteBDEgress: DELETE /proxy/egress/bd/:id — drop an inventory row with no pins.
func (h *Handler) DeleteBDEgress(c *gin.Context) {
	if h == nil || h.runtime == nil {
		respondError(c, http.StatusServiceUnavailable, "egress_unavailable", nil)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = c.ShouldBindJSON(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(c.Query("id"))
	}
	if id == "" {
		respondError(c, http.StatusBadRequest, "invalid body", nil)
		return
	}
	if !proxyutil.IsBDEgressID(id) {
		id = proxyutil.BDEgressID(id)
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	if err := h.runtime.DeleteBDEgress(ctx, id); err != nil {
		respondError(c, http.StatusBadRequest, "bd_egress_delete_failed", err)
		return
	}
	respondOK(c)
}
