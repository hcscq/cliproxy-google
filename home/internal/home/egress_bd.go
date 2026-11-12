package home

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/config"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Bright Data ISP static-IP egress (kind=bd).
//
// Hard rules:
//   - routing.egress.bd.enabled defaults to FALSE. When off, BD egresses are
//     not registered, not health-checked, not shown, and assign is rejected.
//   - Import, auto-balance and pickEgress NEVER select a BD egress. Only an
//     explicit assign (egress_id=bd/<ip>) or a manual panel pick binds one.
//   - Egress down/removed pauses the pinned credential in place; it is never
//     repinned or sent direct. Same rule as self-built egresses.
//   - The Bright Data zone password lives only in a K8s Secret (process env).
//     Credentials store a password-less relay URL plus egress_id / egress_ip /
//     egress_kind=bd. Home and Proxy inject the password at CONNECT time.

const (
	EgressKindBD          = proxyutil.EgressKindBD
	EgressKindKey         = proxyutil.EgressKindKey
	bdRelaysEnv           = "HOME_BD_RELAYS"
	defaultBDProbeURL     = "https://api.ipify.org?format=json"
	defaultBDRelayService = "cliproxy-bd-relay.cliproxy-google.svc.cluster.local:3142"
)

// BDEgressRow is one Bright Data static IP (egress_bd table).
type BDEgressRow struct {
	ID             string
	ExpectedIP     string
	Enabled        bool
	Draining       bool
	MaxCredentials int // 0 treated as 1
	ChainRef       string
}

// EffectiveMax returns the hard per-IP cap (default 1).
func (r BDEgressRow) EffectiveMax() int {
	if r.MaxCredentials <= 0 {
		return 1
	}
	return r.MaxCredentials
}

// BDEgressRepo is the egress_bd subset Home needs (adapter lives in cmd/home).
type BDEgressRepo interface {
	ListBDEgress(ctx context.Context) ([]BDEgressRow, error)
	UpsertBDEgress(ctx context.Context, row BDEgressRow) error
	DeleteBDEgress(ctx context.Context, id string) error
}

func (r *Runtime) SetBDEgressRepo(repo BDEgressRepo) {
	r.egress.mu.Lock()
	r.egress.bdRepo = repo
	r.egress.mu.Unlock()
}

func (r *Runtime) bdRepo() BDEgressRepo {
	r.egress.mu.Lock()
	defer r.egress.mu.Unlock()
	return r.egress.bdRepo
}

// BDEnabled is the hard master switch (routing.egress.bd.enabled, default false).
func (r *Runtime) BDEnabled() bool {
	if r == nil {
		return false
	}
	cfg := r.Config()
	if cfg == nil {
		return false
	}
	return cfg.Routing.Egress.BD.Enabled
}

// BDConfigView is the non-secret BD configuration the panel may display.
type BDConfigView struct {
	Enabled          bool   `json:"enabled"`
	Endpoint         string `json:"endpoint"`
	BaseUsername     string `json:"base_username"`
	UsernameTemplate string `json:"username_template"`
	Zone             string `json:"zone,omitempty"`
	RelayPort        int    `json:"relay_port"`
	RelayService     string `json:"relay_service,omitempty"`
	ProbeURL         string `json:"probe_url,omitempty"`
	HasPassword      bool   `json:"has_password"`
}

// BDSettings returns non-secret BD config plus the inventory (always, so
// operators can prepare IPs while the flag is still off). The password is
// never included.
func (r *Runtime) BDSettings(ctx context.Context) (BDConfigView, []BDEgressRow, error) {
	view := BDConfigView{
		Enabled:          r.BDEnabled(),
		Endpoint:         proxyutil.DefaultBDEndpoint,
		UsernameTemplate: proxyutil.DefaultBDUsernameTemplate,
		RelayPort:        proxyutil.DefaultBDRelayPort,
		RelayService:     defaultBDRelayService,
		ProbeURL:         defaultBDProbeURL,
		HasPassword:      proxyutil.HasBDPassword(),
	}
	if cfg := r.bdCfg(); cfg != nil {
		if cfg.Endpoint != "" {
			view.Endpoint = cfg.Endpoint
		}
		view.BaseUsername = cfg.BaseUsername
		if cfg.UsernameTemplate != "" {
			view.UsernameTemplate = cfg.UsernameTemplate
		}
		view.Zone = cfg.Zone
		if cfg.RelayPort > 0 {
			view.RelayPort = cfg.RelayPort
		}
		if cfg.RelayService != "" {
			view.RelayService = cfg.RelayService
		}
		if cfg.ProbeURL != "" {
			view.ProbeURL = cfg.ProbeURL
		}
	}
	var rows []BDEgressRow
	if repo := r.bdRepo(); repo != nil {
		var err error
		rows, err = repo.ListBDEgress(ctx)
		if err != nil {
			return view, nil, err
		}
	}
	if rows == nil {
		rows = []BDEgressRow{}
	}
	return view, rows, nil
}

func (r *Runtime) bdCfg() *config.RoutingEgressBDConfig {
	cfg := r.Config()
	if cfg == nil {
		return nil
	}
	c := cfg.Routing.Egress.BD
	return &c
}

// ListBDEgress returns the inventory (flag-independent).
func (r *Runtime) ListBDEgress(ctx context.Context) ([]BDEgressRow, error) {
	repo := r.bdRepo()
	if repo == nil {
		return []BDEgressRow{}, nil
	}
	return repo.ListBDEgress(ctx)
}

// UpsertBDEgress validates and stores one inventory row. The zone password is
// never accepted or stored. Allowed while the flag is off (preparation).
func (r *Runtime) UpsertBDEgress(ctx context.Context, row BDEgressRow) (BDEgressRow, error) {
	repo := r.bdRepo()
	if repo == nil {
		return BDEgressRow{}, fmt.Errorf("bd egress repository is not configured")
	}
	ip := strings.TrimSpace(row.ExpectedIP)
	if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
		return BDEgressRow{}, fmt.Errorf("expected_ip %q is not a valid IPv4 address", row.ExpectedIP)
	}
	row.ExpectedIP = ip
	row.ID = proxyutil.BDEgressID(ip)
	row.ChainRef = strings.TrimSpace(row.ChainRef)
	if row.MaxCredentials < 0 {
		row.MaxCredentials = 0
	}
	if err := repo.UpsertBDEgress(ctx, row); err != nil {
		return BDEgressRow{}, err
	}
	// Refresh discovery so a subsequent explicit assign can see the new row
	// (only takes effect when the flag is on).
	r.invalidateEgressDiscovery()
	return row, nil
}

// DeleteBDEgress drops an inventory row. Refused when credentials are still pinned.
func (r *Runtime) DeleteBDEgress(ctx context.Context, id string) error {
	id = proxyutil.BDEgressID(strings.TrimPrefix(strings.TrimSpace(id), proxyutil.BDIDPrefix))
	// If the id was just an IP, BDEgressID wraps it; if it was already bd/ip, also fine.
	if !proxyutil.IsBDEgressID(id) {
		id = proxyutil.BDEgressID(strings.TrimSpace(id))
	}
	if n := r.countPinnedTo(id, ""); n > 0 {
		return fmt.Errorf("egress %s still has %d credentials; move them first (设置出口)", id, n)
	}
	repo := r.bdRepo()
	if repo == nil {
		return fmt.Errorf("bd egress repository is not configured")
	}
	if err := repo.DeleteBDEgress(ctx, id); err != nil {
		return err
	}
	r.invalidateEgressDiscovery()
	return nil
}

func (r *Runtime) invalidateEgressDiscovery() {
	d := &r.egress.disc
	d.mu.Lock()
	d.at = time.Time{}
	d.mu.Unlock()
}

func (r *Runtime) countPinnedTo(id, proxyURL string) int {
	if r.coreManager == nil {
		return 0
	}
	n := 0
	for _, a := range r.coreManager.ListLight(isAntigravity) {
		full := r.fullAuth(context.Background(), a.ID)
		if full == nil {
			continue
		}
		if id != "" && metaString(full, EgressIDKey) == id {
			n++
			continue
		}
		if proxyURL != "" && strings.TrimSpace(full.ProxyURL) == proxyURL {
			n++
		}
	}
	return n
}

// bdEgressSpecs builds EgressSpec values for inventory rows. Returns nil when
// the flag is off (the egresses are not registered).
func (r *Runtime) bdEgressSpecs(ctx context.Context) []EgressSpec {
	if !r.BDEnabled() {
		return nil
	}
	rows, err := r.ListBDEgress(ctx)
	if err != nil {
		log.WithError(err).Warn("egress: list bd inventory")
		return nil
	}
	relay := r.bdRelayHost()
	cfgUser, cfgTemplate := "", ""
	if c := r.bdCfg(); c != nil {
		cfgUser, cfgTemplate = c.BaseUsername, c.UsernameTemplate
	}
	out := make([]EgressSpec, 0, len(rows))
	for _, row := range rows {
		user := proxyutil.RenderBDUsername(cfgUser, cfgTemplate, row.ExpectedIP)
		proxyURL, err := proxyutil.BuildBDRelayURL(user, relay)
		if err != nil {
			log.WithError(err).Warnf("egress: bd relay url for %s", row.ID)
			continue
		}
		// Defence: never let a password slip into a stored/advertised URL.
		proxyURL = proxyutil.StripProxyPassword(proxyURL)
		spec := EgressSpec{
			Name:             row.ID,
			Kind:             EgressKindBD,
			ProxyURL:         proxyURL,
			IP:               row.ExpectedIP,
			PublicIP:         row.ExpectedIP,
			ExpectedPublicIP: row.ExpectedIP,
			ChainNode:        row.ChainRef,
			Draining:         row.Draining,
			MaxCredentials:   row.EffectiveMax(),
			Removed:          !row.Enabled,
		}
		out = append(out, spec)
	}
	return out
}

func (r *Runtime) bdRelayHost() string {
	if raw := strings.TrimSpace(os.Getenv(bdRelaysEnv)); raw != "" {
		// "dns:name:port" or "host:port". For a single concrete host use it;
		// for dns:, resolve and take the first (kube Service / headless).
		if rest, ok := strings.CutPrefix(raw, "dns:"); ok {
			if addrs := resolveAgents(context.Background(), "dns:"+rest); len(addrs) > 0 {
				return addrs[0]
			}
			// Fall through to the name itself so health checks still have a target.
			return rest
		}
		if first, _, _ := strings.Cut(raw, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	if c := r.bdCfg(); c != nil && strings.TrimSpace(c.RelayService) != "" {
		return strings.TrimSpace(c.RelayService)
	}
	return defaultBDRelayService
}

func (r *Runtime) bdProbeURL() string {
	if c := r.bdCfg(); c != nil && strings.TrimSpace(c.ProbeURL) != "" {
		return strings.TrimSpace(c.ProbeURL)
	}
	return defaultBDProbeURL
}

// mergeBDSpecs appends BD egresses when the flag is on. Used by the health
// loop and by knownEgressSpecs.
func mergeBDSpecs(specs []EgressSpec, bd []EgressSpec) []EgressSpec {
	if len(bd) == 0 {
		return specs
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Name] = true
	}
	for _, s := range bd {
		if seen[s.Name] {
			// Replace with the fresh inventory-derived spec.
			for i := range specs {
				if specs[i].Name == s.Name {
					specs[i] = s
					break
				}
			}
			continue
		}
		specs = append(specs, s)
	}
	return specs
}

func isBDEgress(s EgressSpec) bool {
	return s.Kind == EgressKindBD || proxyutil.IsBDEgressID(s.Name)
}

// IsBDEgress reports whether spec is a Bright Data static-IP egress.
func IsBDEgress(s EgressSpec) bool { return isBDEgress(s) }

// checkBDEgress CONNECTs to oauth2.googleapis.com through the relay (password
// injected from the Secret) and then verifies the observed public IP equals
// ExpectedPublicIP. Failures are redacted so the password cannot leak into
// LastError or logs.
func checkBDEgress(ctx context.Context, spec EgressSpec, timeout time.Duration, probeURL, password string) error {
	if timeout <= 0 {
		timeout = egressCheckTimeout
	}
	injected, err := proxyutil.InjectBDPassword(proxyutil.StripProxyPassword(spec.ProxyURL), password)
	if err != nil {
		return fmt.Errorf("bd password: %s", proxyutil.RedactProxyURL(err.Error()))
	}
	if err := egressChecker(ctx, injected, timeout); err != nil {
		return fmt.Errorf("%s", proxyutil.RedactProxyURL(err.Error()))
	}
	expected := strings.TrimSpace(spec.ExpectedPublicIP)
	if expected == "" {
		expected = strings.TrimSpace(spec.PublicIP)
	}
	if expected == "" {
		expected = strings.TrimSpace(spec.IP)
	}
	if probeURL == "" {
		probeURL = defaultBDProbeURL
	}
	observed, err := observeEgressIP(ctx, injected, probeURL, timeout)
	if err != nil {
		return fmt.Errorf("egress ip probe: %s", proxyutil.RedactProxyURL(err.Error()))
	}
	if observed != expected {
		return fmt.Errorf("egress IP mismatch: observed %s want %s", observed, expected)
	}
	return nil
}

// observeEgressIP GETs probeURL through the (already password-injected) proxy
// and parses a public IPv4 from a JSON {"ip":"..."} body or a plain-text IP.
var observeEgressIP = func(ctx context.Context, proxyURL, probeURL string, timeout time.Duration) (string, error) {
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil || transport == nil {
		if err == nil {
			err = fmt.Errorf("proxy transport is nil")
		}
		return "", err
	}
	// The health probe must not follow redirects off-proxy.
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("probe HTTP %d", resp.StatusCode)
	}
	return parseObservedIP(body)
}

func parseObservedIP(body []byte) (string, error) {
	body = []byte(strings.TrimSpace(string(body)))
	var obj struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &obj) == nil {
		if ip := net.ParseIP(strings.TrimSpace(obj.IP)); ip != nil && ip.To4() != nil {
			return ip.To4().String(), nil
		}
	}
	if ip := net.ParseIP(string(body)); ip != nil && ip.To4() != nil {
		return ip.To4().String(), nil
	}
	return "", fmt.Errorf("probe returned no IPv4")
}

// SanitizeEgressStatus strips any password from a status payload (defence in
// depth: BD URLs are stored password-less, but we never return one either).
func SanitizeEgressStatus(st EgressStatus) EgressStatus {
	st.ProxyURL = proxyutil.StripProxyPassword(st.ProxyURL)
	st.LastError = proxyutil.RedactProxyURL(st.LastError)
	return st
}

// SanitizeAuthEgressFields ensures a credential's stored proxy_url and
// metadata never carry a BD password. Applied on assign and on API read.
func SanitizeAuthEgressFields(a *coreauth.Auth) {
	if a == nil {
		return
	}
	a.ProxyURL = proxyutil.StripProxyPassword(a.ProxyURL)
	if a.Metadata != nil {
		if u, ok := a.Metadata["proxy_url"].(string); ok {
			a.Metadata["proxy_url"] = proxyutil.StripProxyPassword(u)
		}
		// Never accept a password field on metadata.
		delete(a.Metadata, "password")
		delete(a.Metadata, "proxy_password")
		delete(a.Metadata, "bd_password")
	}
}

// resolveBDEgress looks up an explicit bd/<ip> assignment target.
// When the flag is off this always errors, even if the inventory has the IP.
func (r *Runtime) resolveBDEgress(ctx context.Context, choice string) (EgressSpec, error) {
	choice = strings.TrimSpace(choice)
	if !r.BDEnabled() {
		return EgressSpec{}, fmt.Errorf("BD 出口未启用（routing.egress.bd.enabled=false）；显式分配也被拒绝 (bd egress is disabled)")
	}
	want := choice
	if !proxyutil.IsBDEgressID(want) {
		if ip := net.ParseIP(want); ip != nil && ip.To4() != nil {
			want = proxyutil.BDEgressID(ip.String())
		}
	}
	for _, s := range r.bdEgressSpecs(ctx) {
		if s.Name == want || s.IP == choice || s.PublicIP == choice {
			if s.Removed {
				return EgressSpec{}, fmt.Errorf("出口 %s 已停用 (bd egress %q is disabled)", s.Name, s.Name)
			}
			if s.Draining {
				return EgressSpec{}, fmt.Errorf("出口 %s 正在排空，拒绝新分配 (bd egress %q is draining)", s.Name, s.Name)
			}
			if r.EgressSettings(ctx).IsDisabled(s) {
				return EgressSpec{}, fmt.Errorf("出口 %s 已停用 (egress %q is disabled)", s.Name, s.Name)
			}
			max := s.MaxCredentials
			if max <= 0 {
				max = 1
			}
			if r.countPinnedTo(s.Name, s.ProxyURL) >= max {
				return EgressSpec{}, fmt.Errorf("出口 %s 已达到 %d 个凭据上限 (bd egress %q is full: max %d)", s.Name, max, s.Name, max)
			}
			return s, nil
		}
	}
	return EgressSpec{}, fmt.Errorf("unknown bd egress %q", choice)
}

func isBDAuth(a *coreauth.Auth) bool {
	if a == nil {
		return false
	}
	id := ""
	if a.Metadata != nil {
		id, _ = a.Metadata[EgressIDKey].(string)
	}
	return proxyutil.IsBDAuth(a.Metadata, id)
}

func isBDChoice(choice string) bool {
	choice = strings.TrimSpace(choice)
	if proxyutil.IsBDEgressID(choice) {
		return true
	}
	// A bare IPv4 is only treated as BD when it matches an inventory row AND
	// the caller went through resolveBDEgress (explicit). Auto never uses this.
	return false
}

func effectiveBDProxyURL(a *coreauth.Auth) (string, error) {
	if a == nil {
		return "", fmt.Errorf("bd auth is nil")
	}
	return proxyutil.EffectiveProxyURL(a.ProxyURL, a.Metadata, loadBDPassword())
}

func loadBDPassword() string { return proxyutil.LoadBDPassword() }
