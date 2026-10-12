package home

import (
	"bufio"
	"context"
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
	IP       string `json:"ip"`
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
}

// SetEgressRepo enables sticky egress management.
type egressPin struct {
	proxy  string
	paused bool
}

const egressPinRefresh = 5 * time.Minute

func (r *Runtime) egressPinFor(ctx context.Context, a *coreauth.Auth, refresh bool) (egressPin, bool) {
	r.egress.mu.Lock()
	pin, ok := r.egress.pins[a.ID]
	r.egress.mu.Unlock()
	if ok && !refresh {
		return pin, true
	}
	full := r.fullAuth(ctx, a.ID)
	if full == nil {
		return pin, ok
	}
	pin = egressPin{proxy: strings.TrimSpace(full.ProxyURL), paused: full.Disabled && metaString(full, EgressPausedReasonKey) == egressPausedDown}
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

func (r *Runtime) SetEgressRepo(repo EgressRepo) {
	r.egress.mu.Lock()
	r.egress.repo = repo
	r.egress.mu.Unlock()
}

func (r *Runtime) startEgressManager(ctx context.Context) {
	specs := ParseEgressSpecs(os.Getenv(egressEnv))
	if len(specs) == 0 {
		return
	}
	go func() {
		seeded := false
		tick := time.NewTicker(egressTick)
		defer tick.Stop()
		for {
			if r.catalogLeader() && r.coreManager != nil {
				if !seeded {
					seeded = r.seedEgressPool(ctx, specs) == nil
				}
				r.egressRound(ctx, specs)
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
	for _, s := range specs {
		upd := EgressPoolItem{Name: s.Name, ProxyURL: s.ProxyURL, Enabled: true, Scope: EgressScope, Note: "egress IP " + s.IP}
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
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", egressHealthTarget, egressHealthTarget); err != nil {
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

// pickEgress returns the least-loaded healthy egress (ties by name).
func pickEgress(specs []EgressSpec, status map[string]*EgressStatus, counts map[string]int) (EgressSpec, bool) {
	var best EgressSpec
	found := false
	for _, s := range specs {
		if st := status[s.ProxyURL]; st != nil && !st.Healthy {
			continue
		}
		if !found || counts[s.ProxyURL] < counts[best.ProxyURL] || (counts[s.ProxyURL] == counts[best.ProxyURL] && s.Name < best.Name) {
			best, found = s, true
		}
	}
	return best, found
}

func (r *Runtime) egressRound(ctx context.Context, specs []EgressSpec) {
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
	for k, v := range r.egress.status {
		c := *v
		c.Credentials, c.Paused = 0, 0
		status[k] = &c
	}
	r.egress.mu.Unlock()
	known := map[string]EgressSpec{}
	for _, s := range specs {
		known[s.ProxyURL] = s
	}
	auths := r.coreManager.List()
	sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
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
			spec, ok := pickEgress(specs, status, counts)
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
			continue // manual proxy: respected, not managed
		}
		st := status[pin.proxy]
		if st != nil {
			st.Credentials++
		}
		switch {
		case st != nil && !st.Healthy && !a.Disabled:
			if r.setEgressPause(ctx, a.ID, spec, true, now) {
				r.setPin(a.ID, egressPin{proxy: pin.proxy, paused: true})
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
		if full.Disabled {
			return false
		}
		full.Disabled = true
		full.Status = coreauth.StatusDisabled
		full.StatusMessage = "已暂停 · 出口离线 " + spec.IP
		full.Metadata["disabled"] = true
		full.Metadata[EgressPausedReasonKey] = egressPausedDown
	} else {
		if !full.Disabled || metaString(full, EgressPausedReasonKey) != egressPausedDown {
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
func (r *Runtime) PickEgressForImport(ctx context.Context) (EgressSpec, bool) {
	specs := ParseEgressSpecs(os.Getenv(egressEnv))
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
	return pickEgress(specs, status, counts)
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
