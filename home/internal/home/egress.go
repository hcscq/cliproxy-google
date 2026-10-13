package home

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/proxyutil"
	log "github.com/sirupsen/logrus"
)

// Sticky egress: every Antigravity credential is pinned to one egress proxy (our own
// per-node squid bound to that node's dedicated secondary IP), so all of its Google-facing
// traffic (proxy AI requests, refresh, quota probes, onboarding) leaves from one fixed IP.
// The pin is the credential's proxy_url (stored in Postgres with the auth, synced to proxies
// like any other auth field, so there is no per-request lookup). A credential whose egress
// is down is paused (disabled with a reason) and resumed when the egress is healthy again;
// it is never moved to another egress or to direct egress (fail closed).

const (
	EgressScope           = "egress"
	EgressIDKey           = "egress_id"
	EgressIPKey           = "egress_ip"
	EgressPausedReasonKey = "auto_paused_reason"
	egressPausedDown      = "egress_down"
	egressStatusKV        = "egress-status"
	egressTick            = 5 * time.Second
	egressFailThreshold   = 3
	egressCheckTimeout    = 4 * time.Second
	egressHealthTarget    = "oauth2.googleapis.com:443"
	egressStatusTTL       = 10 * time.Minute
	egressEnv             = "HOME_EGRESS_PROXIES"
)

// EgressSpec is one configured egress: name=proxyURL=publicIP.
type EgressSpec struct {
	Name     string `json:"name"`
	ProxyURL string `json:"proxy_url"`
	IP       string `json:"ip"` // local source IP (identity)
	// Discovered egresses only.
	Node             string `json:"node,omitempty"`
	Primary          bool   `json:"primary,omitempty"`
	PublicIP         string `json:"public_ip,omitempty"`
	Removed          bool   `json:"removed,omitempty"`
	PublicChanged    bool   `json:"public_changed,omitempty"`
	ExpectedPublicIP string `json:"expected_public_ip,omitempty"`
}

// ParseEgressSpecs parses "name=http://host:port=ip,name2=...".
func ParseEgressSpecs(raw string) []EgressSpec {
	var out []EgressSpec
	for _, item := range strings.Split(raw, ",") {
		parts := strings.Split(strings.TrimSpace(item), "=")
		if len(parts) != 3 {
			continue
		}
		spec := EgressSpec{Name: strings.TrimSpace(parts[0]), ProxyURL: strings.TrimSpace(parts[1]), IP: strings.TrimSpace(parts[2])}
		if spec.Name == "" || spec.IP == "" {
			continue
		}
		if u, err := url.Parse(spec.ProxyURL); err != nil || u.Host == "" {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// EgressStatus is the live state of one egress (shared through KV for every Home replica).
type EgressStatus struct {
	EgressSpec
	ID          string    `json:"id"`
	Healthy     bool      `json:"healthy"`
	Failures    int       `json:"failures"`
	LastCheck   time.Time `json:"last_check"`
	LastError   string    `json:"last_error,omitempty"`
	Credentials int       `json:"credentials"`
	Paused      int       `json:"paused"`
}

// EgressPoolItem is one proxy_pool row as Home sees it.
type EgressPoolItem struct {
	ID, Name, ProxyURL, Scope, Note string
	Enabled                         bool
}

// EgressRepo is the proxy_pool subset Home needs (adapter lives in cmd/home).
type EgressRepo interface {
	ListEgressPool(ctx context.Context) ([]EgressPoolItem, error)
	CreateEgressPool(ctx context.Context, item EgressPoolItem) error
	UpdateEgressPool(ctx context.Context, item EgressPoolItem) error
	MarkEgressResult(ctx context.Context, id string, healthy bool, at time.Time) error
}

type egressState struct {
	mu     sync.Mutex
	repo   EgressRepo
	status map[string]*EgressStatus // by proxy URL
	// pins caches each credential's full-auth proxy_url / pause flag; the manager's
	// minimal projections omit them. Refreshed from Postgres every egressPinRefresh.
	pins   map[string]egressPin
	pinsAt time.Time
	disc   egressDiscovery
}

func isEgressPauseReason(reason string) bool {
	return reason == egressPausedDown || reason == egressPausedRemoved
}

// SetEgressRepo enables sticky egress management.
type egressPin struct {
	proxy   string
	paused  bool
	reason  string
	version int64
}

func pauseReasonFor(s EgressSpec) string {
	if s.Removed {
		return egressPausedRemoved
	}
	return egressPausedDown
}

const egressPinRefresh = 5 * time.Minute

func (r *Runtime) egressPinFor(ctx context.Context, a *coreauth.Auth, refresh bool) (egressPin, bool) {
	r.egress.mu.Lock()
	pin, ok := r.egress.pins[a.ID]
	r.egress.mu.Unlock()
	if ok && !refresh && (a.StateVersion == 0 || pin.version == a.StateVersion) {
		return pin, true
	}
	full := r.fullAuth(ctx, a.ID)
	if full == nil {
		return pin, ok
	}
	pin = egressPin{proxy: strings.TrimSpace(full.ProxyURL), paused: full.Disabled && isEgressPauseReason(metaString(full, EgressPausedReasonKey)), reason: metaString(full, EgressPausedReasonKey), version: a.StateVersion}
	r.setPin(a.ID, pin)
	return pin, true
}

func (r *Runtime) setPin(id string, pin egressPin) {
	r.egress.mu.Lock()
	if r.egress.pins == nil {
		r.egress.pins = map[string]egressPin{}
	}
	r.egress.pins[id] = pin
	r.egress.mu.Unlock()
}

func (r *Runtime) hasEgressRepo() bool {
	r.egress.mu.Lock()
	defer r.egress.mu.Unlock()
	return r.egress.repo != nil
}

func (r *Runtime) SetEgressRepo(repo EgressRepo) {
	r.egress.mu.Lock()
	r.egress.repo = repo
	r.egress.mu.Unlock()
}

func (r *Runtime) startEgressManager(ctx context.Context) {
	if strings.TrimSpace(os.Getenv(egressEnv)) == "" && strings.TrimSpace(os.Getenv(egressAgentsEnv)) == "" {
		return
	}
	go func() {
		seededKey := ""
		tick := time.NewTicker(egressTick)
		defer tick.Stop()
		for {
			if r.catalogLeader() && r.coreManager != nil {
				specs, legacy := r.currentEgressSpecs(ctx, false)
				// repo is wired after start; seed only once it exists.
				if key := specsKey(specs); key != seededKey && r.hasEgressRepo() {
					if errSeed := r.seedEgressPool(ctx, specs); errSeed != nil {
						log.WithError(errSeed).Warn("egress: seed proxy pool")
					} else {
						seededKey = key
					}
				}
				r.egressRoundWith(ctx, specs, legacy)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

func (r *Runtime) seedEgressPool(ctx context.Context, specs []EgressSpec) error {
	r.egress.mu.Lock()
	repo := r.egress.repo
	r.egress.mu.Unlock()
	if repo == nil {
		return nil
	}
	items, err := repo.ListEgressPool(ctx)
	if err != nil {
		return err
	}
	byName := map[string]EgressPoolItem{}
	for _, it := range items {
		if it.Scope == EgressScope {
			byName[it.Name] = it
		}
	}
	// Drop stale egress-scope rows (e.g. squid-era "egress-<node>") no longer in specs.
	if del, ok := repo.(egressPoolDeleter); ok && len(specs) > 0 {
		want := map[string]bool{}
		for _, s := range specs {
			want[s.Name] = true
		}
		for name, it := range byName {
			if !want[name] {
				if errDel := del.DeleteEgressPool(ctx, it.ID); errDel != nil {
					log.WithError(errDel).Warnf("egress: delete stale pool row %s", name)
				} else {
					log.Infof("egress: deleted stale pool row %s", name)
				}
			}
		}
	}
	for _, s := range specs {
		upd := EgressPoolItem{Name: s.Name, ProxyURL: s.ProxyURL, Enabled: true, Scope: EgressScope, Note: egressNote(s)}
		if cur, ok := byName[s.Name]; ok {
			if cur.ProxyURL != s.ProxyURL || cur.Note != upd.Note || !cur.Enabled {
				upd.ID = cur.ID
				if err := repo.UpdateEgressPool(ctx, upd); err != nil {
					return err
				}
			}
			continue
		}
		if err := repo.CreateEgressPool(ctx, upd); err != nil {
			return err
		}
	}
	return nil
}

// egressPoolDeleter is optionally implemented by the egress repo.
type egressPoolDeleter interface {
	DeleteEgressPool(ctx context.Context, id string) error
}

// checkEgress opens a CONNECT tunnel through the egress to a Google endpoint.
func checkEgress(ctx context.Context, proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, egressCheckTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(cctx, "tcp", u.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(egressCheckTimeout))
	auth := ""
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)) + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", egressHealthTarget, egressHealthTarget, auth); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CONNECT %d", resp.StatusCode)
	}
	return nil
}

var egressChecker = checkEgress

func isAntigravity(a *coreauth.Auth) bool {
	return a != nil && strings.EqualFold(a.Provider, "antigravity")
}

var errEgressRemoved = errors.New("IP 已移除")

// repointEgress moves a credential to another address of the SAME exit IP.
func (r *Runtime) repointEgress(ctx context.Context, id, from string, spec EgressSpec, now time.Time) bool {
	full := r.fullAuth(ctx, id)
	if full == nil || strings.TrimSpace(full.ProxyURL) != from {
		return false
	}
	ApplyEgressMetadata(full, spec) // lifts only egress pauses; other disables stay
	full.UpdatedAt = now
	if _, err := r.coreManager.Update(ctx, full); err != nil {
		log.WithError(err).Warnf("egress: repoint %s failed", id)
		return false
	}
	log.Infof("egress: %s (%s) repointed %s -> %s (same IP %s)", id, full.Label, from, spec.Name, spec.IP)
	return true
}

// pickEgress returns the least-loaded healthy egress (ties by name).
func pickEgress(specs []EgressSpec, status map[string]*EgressStatus, counts map[string]int, allowPrimary bool) (EgressSpec, bool) {
	var best EgressSpec
	found := false
	for _, s := range specs {
		if s.Removed || (s.Primary && !allowPrimary) {
			continue
		}
		if st := status[s.ProxyURL]; st != nil && !st.Healthy {
			continue
		}
		if !found || counts[s.ProxyURL] < counts[best.ProxyURL] || (counts[s.ProxyURL] == counts[best.ProxyURL] && s.Name < best.Name) {
			best, found = s, true
		}
	}
	return best, found
}

func specsKey(specs []EgressSpec) string {
	var b strings.Builder
	for _, s := range specs {
		b.WriteString(s.Name + "=" + s.ProxyURL + ";")
	}
	return b.String()
}

func (r *Runtime) egressRound(ctx context.Context, specs []EgressSpec) {
	r.egressRoundWith(ctx, specs, nil)
}

// egressNote is the proxy_pool note for an egress.
func egressNote(s EgressSpec) string {
	if s.PublicIP != "" && s.PublicIP != s.IP {
		return "egress IP " + s.IP + " (public " + s.PublicIP + ")"
	}
	return "egress IP " + s.IP
}

func (r *Runtime) egressRoundWith(ctx context.Context, specs []EgressSpec, legacy map[string]EgressSpec) {
	now := time.Now().UTC()
	r.egress.mu.Lock()
	if r.egress.status == nil {
		r.egress.status = map[string]*EgressStatus{}
	}
	repo := r.egress.repo
	r.egress.mu.Unlock()

	// 1. Health (parallel, so one dead egress never delays the others).
	errs := make([]error, len(specs))
	var wg sync.WaitGroup
	for i := range specs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if specs[i].Removed {
				errs[i] = errEgressRemoved
				return
			}
			errs[i] = egressChecker(ctx, specs[i].ProxyURL)
		}(i)
	}
	wg.Wait()
	for i, s := range specs {
		err := errs[i]
		r.egress.mu.Lock()
		st := r.egress.status[s.ProxyURL]
		if st == nil {
			st = &EgressStatus{EgressSpec: s, ID: s.Name, Healthy: true}
			r.egress.status[s.ProxyURL] = st
		}
		st.EgressSpec = s
		if s.Removed {
			st.Failures = egressFailThreshold - 1 // removed is final: unhealthy now
		}
		was := st.Healthy
		st.LastCheck = now
		if err == nil {
			st.Failures, st.Healthy, st.LastError = 0, true, ""
		} else {
			st.Failures++
			st.LastError = err.Error()
			if st.Failures >= egressFailThreshold {
				st.Healthy = false
			}
		}
		changed := was != st.Healthy
		healthy := st.Healthy
		r.egress.mu.Unlock()
		if changed {
			log.Warnf("egress %s (%s) healthy=%v: %v", s.Name, s.IP, healthy, err)
			if repo != nil {
				if items, errList := repo.ListEgressPool(ctx); errList == nil {
					for _, it := range items {
						if it.Scope == EgressScope && it.Name == s.Name {
							_ = repo.MarkEgressResult(ctx, it.ID, healthy, now)
						}
					}
				}
			}
		}
	}

	// 2. Assign, pause, resume.
	r.egress.mu.Lock()
	status := map[string]*EgressStatus{}
	for _, sp := range specs {
		if v := r.egress.status[sp.ProxyURL]; v != nil {
			c := *v
			c.Credentials, c.Paused = 0, 0
			status[sp.ProxyURL] = &c
		}
	}
	r.egress.mu.Unlock()
	known := map[string]EgressSpec{}
	for _, s := range specs {
		known[s.ProxyURL] = s
	}
	auths := r.coreManager.List()
	sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	allowPrimary := r.EgressSettings(ctx).AllowPrimary
	r.egress.mu.Lock()
	refresh := now.Sub(r.egress.pinsAt) >= egressPinRefresh
	if refresh {
		r.egress.pinsAt = now
	}
	r.egress.mu.Unlock()
	pins := map[string]egressPin{}
	counts := map[string]int{}
	for _, a := range auths {
		if !isAntigravity(a) {
			continue
		}
		pin, ok := r.egressPinFor(ctx, a, refresh)
		if !ok {
			continue
		}
		pins[a.ID] = pin
		if pin.proxy != "" {
			counts[pin.proxy]++
		}
	}
	for _, a := range auths {
		pin, ok := pins[a.ID]
		if !ok {
			continue
		}
		if pin.proxy == "" {
			spec, ok := pickEgress(specs, status, counts, allowPrimary)
			if !ok {
				continue // no healthy egress: leave unassigned, try next round
			}
			if r.assignEgress(ctx, a.ID, spec, now) {
				counts[spec.ProxyURL]++
				r.setPin(a.ID, egressPin{proxy: spec.ProxyURL})
				if st := status[spec.ProxyURL]; st != nil {
					st.Credentials++
				}
			} else {
				r.egressPinFor(ctx, a, true)
			}
			continue
		}
		spec, ours := known[pin.proxy]
		if !ours {
			// Same exit IP, new address (legacy squid -> agent, or agent moved): repoint.
			target, ok := legacy[pin.proxy]
			if !ok {
				if ip := egressURLIP(pin.proxy); ip != "" {
					for _, sp := range specs {
						if sp.IP == ip && !sp.Removed && egressURLIP(sp.ProxyURL) == ip {
							target, ok = sp, true
							break
						}
					}
				}
			}
			if !ok {
				continue // manual proxy: respected, not managed
			}
			if !r.repointEgress(ctx, a.ID, pin.proxy, target, now) {
				continue
			}
			counts[pin.proxy]--
			counts[target.ProxyURL]++
			pin.proxy = target.ProxyURL
			r.setPin(a.ID, pin)
			spec = target
		}
		st := status[pin.proxy]
		if st != nil {
			st.Credentials++
		}
		switch {
		case st != nil && !st.Healthy && (!a.Disabled || (pin.paused && pin.reason != pauseReasonFor(spec))):
			if r.setEgressPause(ctx, a.ID, spec, true, now) {
				r.setPin(a.ID, egressPin{proxy: pin.proxy, paused: true, reason: pauseReasonFor(spec)})
			}
			st.Paused++
		case pin.paused && (st == nil || st.Healthy):
			if r.setEgressPause(ctx, a.ID, spec, false, now) {
				r.setPin(a.ID, egressPin{proxy: pin.proxy})
			} else {
				r.egressPinFor(ctx, a, true)
			}
		case pin.paused:
			st.Paused++
		}
	}
	list := make([]*EgressStatus, 0, len(specs))
	for _, s := range specs {
		if st := status[s.ProxyURL]; st != nil {
			list = append(list, st)
		}
	}
	if raw, err := json.Marshal(list); err == nil {
		_, _ = r.KVSet(ctx, egressStatusKV, raw, egressStatusTTL, "")
	}
}

func metaString(a *coreauth.Auth, key string) string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	s, _ := a.Metadata[key].(string)
	return s
}

func (r *Runtime) assignEgress(ctx context.Context, id string, spec EgressSpec, now time.Time) bool {
	full := r.fullAuth(ctx, id)
	if full == nil || strings.TrimSpace(full.ProxyURL) != "" {
		return false
	}
	if full.Metadata == nil {
		full.Metadata = map[string]any{}
	}
	full.ProxyURL = spec.ProxyURL
	full.Metadata["proxy_url"] = spec.ProxyURL
	full.Metadata[EgressIDKey] = spec.Name
	full.Metadata[EgressIPKey] = spec.IP
	full.UpdatedAt = now
	if _, err := r.coreManager.Update(ctx, full); err != nil {
		log.WithError(err).Warnf("egress: assign %s failed", id)
		return false
	}
	log.Infof("egress: %s (%s) pinned to %s (%s)", id, full.Label, spec.Name, spec.IP)
	return true
}

func (r *Runtime) setEgressPause(ctx context.Context, id string, spec EgressSpec, pause bool, now time.Time) bool {
	full := r.fullAuth(ctx, id)
	if full == nil {
		return false
	}
	if full.Metadata == nil {
		full.Metadata = map[string]any{}
	}
	if pause {
		reason := pauseReasonFor(spec)
		cur := metaString(full, EgressPausedReasonKey)
		if full.Disabled && (!isEgressPauseReason(cur) || cur == reason) {
			return false // manual / account-block disable, or already paused for this reason
		}
		full.Disabled = true
		full.Status = coreauth.StatusDisabled
		if spec.Removed {
			full.StatusMessage = "已暂停 · IP 已移除 " + spec.IP
		} else {
			full.StatusMessage = "已暂停 · 出口离线 " + spec.IP
		}
		full.Metadata["disabled"] = true
		full.Metadata[EgressPausedReasonKey] = reason
	} else {
		if !full.Disabled || !isEgressPauseReason(metaString(full, EgressPausedReasonKey)) {
			return false
		}
		full.Disabled = false
		full.Status = coreauth.StatusActive
		full.StatusMessage = ""
		full.Metadata["disabled"] = false
		delete(full.Metadata, EgressPausedReasonKey)
	}
	full.UpdatedAt = now
	if _, err := r.coreManager.Update(ctx, full); err != nil {
		log.WithError(err).Warnf("egress: pause=%v %s failed", pause, id)
		return false
	}
	log.Warnf("egress: %s (%s) paused=%v (egress %s %s)", id, full.Label, pause, spec.Name, spec.IP)
	return true
}

// EgressStatuses returns the latest egress list (from KV, any replica).
func (r *Runtime) EgressStatuses(ctx context.Context) ([]EgressStatus, error) {
	raw, ok, err := r.KVGet(ctx, egressStatusKV)
	if err != nil || !ok {
		return nil, err
	}
	var out []EgressStatus
	return out, json.Unmarshal(raw, &out)
}

// PickEgressForImport returns the least-loaded healthy egress for a credential that is
// being created (RT import), so its enrichment calls already use that egress.
func (r *Runtime) PickEgressForImport(ctx context.Context, allowPrimary *bool) (EgressSpec, bool) {
	specs := r.knownEgressSpecs(ctx)
	if len(specs) == 0 {
		return EgressSpec{}, false
	}
	status := map[string]*EgressStatus{}
	if list, err := r.EgressStatuses(ctx); err == nil {
		for i := range list {
			status[list[i].ProxyURL] = &list[i]
		}
	}
	counts := map[string]int{}
	if r.coreManager != nil {
		for _, a := range r.coreManager.List() {
			if isAntigravity(a) && a.ProxyURL != "" {
				counts[a.ProxyURL]++
			}
		}
	}
	return pickEgress(specs, status, counts, r.allowPrimary(ctx, allowPrimary))
}

// --- per-credential HTTP clients (fail closed) ---

var (
	egressTransportsMu sync.Mutex
	egressTransports   = map[string]http.RoundTripper{}
	errEgressProxy     = errors.New("credential egress proxy unusable")
)

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// credentialHTTPClient returns a client that uses the credential's proxy_url when set.
// A broken proxy URL fails every request instead of silently going direct.
func credentialHTTPClient(auth *coreauth.Auth, timeout time.Duration) *http.Client {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" {
		return antigravityHTTPClient
	}
	egressTransportsMu.Lock()
	defer egressTransportsMu.Unlock()
	rt := egressTransports[proxyURL]
	if rt == nil {
		transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
		if err != nil || transport == nil {
			rt = failingTransport{err: errEgressProxy}
		} else {
			transport.MaxIdleConnsPerHost = 64
			transport.IdleConnTimeout = 90 * time.Second
			rt = transport
		}
		egressTransports[proxyURL] = rt
	}
	return &http.Client{Timeout: timeout, Transport: rt}
}

// EgressAuto selects the least-loaded healthy egress.
const EgressAuto = "auto"

// ResolveEgress maps a panel/API choice to an egress: "" or "auto" picks the
// least-loaded healthy one (ok=false when none is healthy); otherwise the egress
// with that name (an unknown name is an error).
//
// allowPrimary (nil = global default) lets auto use node primary IPs; when it is off
// and no healthy secondary exists, auto fails instead of silently using a primary.
func (r *Runtime) ResolveEgress(ctx context.Context, choice string, allowPrimary *bool) (EgressSpec, bool, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" || strings.EqualFold(choice, EgressAuto) {
		spec, ok := r.PickEgressForImport(ctx, allowPrimary)
		if !ok && len(r.knownEgressSpecs(ctx)) > 0 {
			return EgressSpec{}, false, NoEgressError(r.allowPrimary(ctx, allowPrimary))
		}
		return spec, ok, nil
	}
	for _, s := range r.knownEgressSpecs(ctx) {
		if s.Name == choice || s.IP == choice || (s.PublicIP != "" && s.PublicIP == choice) {
			if s.Removed {
				return EgressSpec{}, false, fmt.Errorf("egress %q: IP removed", choice)
			}
			return s, true, nil
		}
	}
	return EgressSpec{}, false, fmt.Errorf("unknown egress %q", choice)
}

// ApplyEgressMetadata pins (spec set) or unpins (zero spec: auto-balance on the
// next round) an auth in place.
func ApplyEgressMetadata(a *coreauth.Auth, spec EgressSpec) {
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	a.ProxyURL = spec.ProxyURL
	a.Metadata["proxy_url"] = spec.ProxyURL
	if spec.ProxyURL == "" {
		delete(a.Metadata, EgressIDKey)
		delete(a.Metadata, EgressIPKey)
	} else {
		a.Metadata[EgressIDKey] = spec.Name
		a.Metadata[EgressIPKey] = spec.IP
	}
	// A pause belongs to the old egress; the new one's health decides next round.
	if isEgressPauseReason(metaString(a, EgressPausedReasonKey)) {
		delete(a.Metadata, EgressPausedReasonKey)
		a.Disabled = false
		a.Status = coreauth.StatusActive
		a.StatusMessage = ""
		a.Metadata["disabled"] = false
	}
}

// knownEgressSpecs lists egresses on any replica: the leader's KV status list, else
// the static configuration.
func (r *Runtime) knownEgressSpecs(ctx context.Context) []EgressSpec {
	if list, err := r.EgressStatuses(ctx); err == nil && len(list) > 0 {
		out := make([]EgressSpec, 0, len(list))
		for _, st := range list {
			out = append(out, st.EgressSpec)
		}
		return out
	}
	return ParseEgressSpecs(os.Getenv(egressEnv))
}

// NoEgressError explains why auto-balance found nothing.
func NoEgressError(allowPrimary bool) error {
	if allowPrimary {
		return errors.New("没有可用的健康出口 (no healthy egress)")
	}
	return errors.New("没有可用的健康副 IP 出口；如需使用主 IP 请开启「允许分配到主 IP」(no healthy secondary egress; enable allow_primary to use node primary IPs)")
}

// EgressSettingsValue is the global egress policy (Settings page).
type EgressSettingsValue struct {
	// AllowPrimary lets automatic assignment use node primary IPs (default off).
	AllowPrimary bool `json:"allow_primary"`
}

const egressSettingsKV = "egress-settings"

var egressSettingsCache struct {
	sync.Mutex
	at  time.Time
	val EgressSettingsValue
}

// EgressSettings returns the global egress policy (cached 10s; default secondaries only).
func (r *Runtime) EgressSettings(ctx context.Context) EgressSettingsValue {
	egressSettingsCache.Lock()
	defer egressSettingsCache.Unlock()
	if time.Since(egressSettingsCache.at) < 10*time.Second {
		return egressSettingsCache.val
	}
	var v EgressSettingsValue
	if raw, ok, err := r.KVGet(ctx, egressSettingsKV); err == nil && ok {
		_ = json.Unmarshal(raw, &v)
	} else if err != nil && !egressSettingsCache.at.IsZero() {
		return egressSettingsCache.val
	}
	egressSettingsCache.at, egressSettingsCache.val = time.Now(), v
	return v
}

// SetEgressSettings stores the global egress policy.
func (r *Runtime) SetEgressSettings(ctx context.Context, v EgressSettingsValue) error {
	raw, _ := json.Marshal(v)
	if _, err := r.KVSet(ctx, egressSettingsKV, raw, egressRegistryTTL, ""); err != nil {
		return err
	}
	egressSettingsCache.Lock()
	egressSettingsCache.at, egressSettingsCache.val = time.Now(), v
	egressSettingsCache.Unlock()
	return nil
}

func (r *Runtime) allowPrimary(ctx context.Context, override *bool) bool {
	if override != nil {
		return *override
	}
	return r.EgressSettings(ctx).AllowPrimary
}
