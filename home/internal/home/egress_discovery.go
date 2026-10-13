package home

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Dynamic egresses: every node runs an egress agent (cmd/egress-agent) that reports
// its public IPv4 addresses and their probed public exit IPs. Each (node, IP) is one
// egress with a stable id "<node>/<ip>"; the proxy URL selects the IP through the
// proxy username: http://<ip>@<agent>:<port>. Home keeps a registry of every egress it
// has seen (KV), so an IP that disappears from its node shows as removed (its
// credentials are paused, never moved) instead of silently vanishing.

const (
	egressAgentsEnv       = "HOME_EGRESS_AGENTS" // "dns:<name>:<port>" or "host:port,host:port"
	egressRegistryKV      = "egress-registry"
	egressRegistryTTL     = 10 * 365 * 24 * time.Hour
	egressDiscoverEvery   = 30 * time.Second
	egressPausedRemoved   = "egress_removed"
	egressInventoryTimout = 4 * time.Second
)

// agentInventory mirrors egressagent.Inventory (no import: keeps home lean).
type agentInventory struct {
	Node string `json:"node"`
	IPs  []struct {
		IP         string    `json:"ip"`
		Primary    bool      `json:"primary"`
		PublicIP   string    `json:"public_ip"`
		ProbedAt   time.Time `json:"probed_at"`
		ProbeError string    `json:"probe_error"`
	} `json:"ips"`
}

// egressRegistryEntry is what Home remembers about one dynamic egress.
type egressRegistryEntry struct {
	Spec           EgressSpec `json:"spec"`
	ExpectedPublic string     `json:"expected_public,omitempty"`
	LastSeen       time.Time  `json:"last_seen"`
}

type egressDiscovery struct {
	mu       sync.Mutex
	at       time.Time
	specs    []EgressSpec
	legacy   map[string]EgressSpec // legacy (static) proxy URL -> replacement dynamic spec (same IP)
	registry map[string]*egressRegistryEntry
	loaded   bool
}

var egressHTTP = &http.Client{Timeout: egressInventoryTimout, Transport: &http.Transport{Proxy: nil}}

// fetchInventory is replaceable in tests.
var fetchInventory = func(ctx context.Context, agent string) (agentInventory, error) {
	var inv agentInventory
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+agent+"/inventory", nil)
	resp, err := egressHTTP.Do(req)
	if err != nil {
		return inv, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return inv, fmt.Errorf("inventory %d", resp.StatusCode)
	}
	return inv, json.NewDecoder(resp.Body).Decode(&inv)
}

// resolveAgents is replaceable in tests.
var resolveAgents = func(ctx context.Context, raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(item, "dns:"); ok {
			host, port, err := net.SplitHostPort(rest)
			if err != nil {
				continue
			}
			addrs, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				log.WithError(err).Warn("egress: agent DNS lookup failed")
				continue
			}
			for _, a := range addrs {
				out = append(out, net.JoinHostPort(a, port))
			}
			continue
		}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func shortNode(node string) string {
	return strings.TrimPrefix(node, "rainyun-")
}

// egressURLIP returns the source IP selected by a dynamic egress URL (its username).
func egressURLIP(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return ""
	}
	if ip := net.ParseIP(u.User.Username()); ip != nil && ip.To4() != nil {
		return ip.String()
	}
	return ""
}

func (r *Runtime) loadEgressRegistry(ctx context.Context) {
	// Reloaded every discovery so panel actions on any replica (ack / forget) stick.
	d := &r.egress.disc
	if _, errStore := r.kvStore(); errStore != nil {
		if d.registry == nil { // no cluster store (single node / tests): in-memory only
			d.registry, d.loaded = map[string]*egressRegistryEntry{}, true
		}
		return
	}
	raw, ok, err := r.KVGet(ctx, egressRegistryKV)
	if err != nil {
		return // keep the last registry; never treat a KV error as "no egresses"
	}
	reg := map[string]*egressRegistryEntry{}
	if ok {
		var entries []*egressRegistryEntry
		if json.Unmarshal(raw, &entries) == nil {
			for _, e := range entries {
				reg[e.Spec.Name] = e
			}
		}
	}
	d.registry, d.loaded = reg, true
}

func (r *Runtime) saveEgressRegistry(ctx context.Context) {
	d := &r.egress.disc
	entries := make([]*egressRegistryEntry, 0, len(d.registry))
	for _, e := range d.registry {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Spec.Name < entries[j].Spec.Name })
	if raw, err := json.Marshal(entries); err == nil {
		_, _ = r.KVSet(ctx, egressRegistryKV, raw, egressRegistryTTL, "")
	}
}

// currentEgressSpecs returns static (legacy) + discovered egresses, refreshing the
// discovery at most every egressDiscoverEvery.
func (r *Runtime) currentEgressSpecs(ctx context.Context, force bool) ([]EgressSpec, map[string]EgressSpec) {
	d := &r.egress.disc
	d.mu.Lock()
	defer d.mu.Unlock()
	if !force && d.loaded && time.Since(d.at) < egressDiscoverEvery {
		return d.specs, d.legacy
	}
	static := ParseEgressSpecs(os.Getenv(egressEnv))
	agentsRaw := strings.TrimSpace(os.Getenv(egressAgentsEnv))
	if agentsRaw == "" {
		d.specs, d.legacy, d.at, d.loaded = static, nil, time.Now(), true
		return d.specs, d.legacy
	}
	r.loadEgressRegistry(ctx)
	if d.registry == nil {
		return d.specs, d.legacy
	}
	agents := resolveAgents(ctx, agentsRaw)
	type res struct {
		agent string
		inv   agentInventory
		err   error
	}
	results := make([]res, len(agents))
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func(i int, a string) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, egressInventoryTimout)
			defer cancel()
			inv, err := fetchInventory(cctx, a)
			results[i] = res{a, inv, err}
		}(i, a)
	}
	wg.Wait()
	now := time.Now().UTC()
	changed := false
	reachableNodes := map[string]bool{}
	present := map[string]bool{}
	for _, rs := range results {
		if rs.err != nil || rs.inv.Node == "" {
			if rs.err != nil {
				log.WithError(rs.err).Debugf("egress: agent %s unreachable", rs.agent)
			}
			continue
		}
		reachableNodes[rs.inv.Node] = true
		for _, ip := range rs.inv.IPs {
			id := shortNode(rs.inv.Node) + "/" + ip.IP
			present[id] = true
			spec := EgressSpec{Name: id, ProxyURL: "http://" + ip.IP + "@" + rs.agent, IP: ip.IP, Node: rs.inv.Node, Primary: ip.Primary, PublicIP: ip.PublicIP}
			e := d.registry[id]
			if e == nil {
				e = &egressRegistryEntry{}
				d.registry[id] = e
				log.Infof("egress: discovered %s (public %s)", id, ip.PublicIP)
				changed = true
			}
			if e.ExpectedPublic == "" && ip.PublicIP != "" {
				e.ExpectedPublic = ip.PublicIP
				changed = true
			}
			if ip.PublicIP != "" && e.ExpectedPublic != "" && ip.PublicIP != e.ExpectedPublic {
				spec.PublicChanged, spec.ExpectedPublicIP = true, e.ExpectedPublic
				if !e.Spec.PublicChanged || e.Spec.PublicIP != ip.PublicIP {
					log.Warnf("egress: %s public IP changed %s -> %s (credentials keep running; acknowledge in panel)", id, e.ExpectedPublic, ip.PublicIP)
				}
			}
			if e.Spec != spec {
				changed = true
			}
			e.Spec, e.LastSeen = spec, now
		}
	}
	specs := make([]EgressSpec, 0, len(d.registry))
	byIP := map[string]EgressSpec{}
	for id, e := range d.registry {
		s := e.Spec
		if !present[id] {
			if reachableNodes[s.Node] && !s.Removed {
				log.Warnf("egress: %s IP removed from node", id)
				s.Removed, changed = true, true
			} else if reachableNodes[s.Node] {
				s.Removed = true
			}
			// unreachable agent: keep last state; the health check decides.
			e.Spec = s
		} else if s.Removed {
			s.Removed, changed = false, true
			e.Spec = s
		}
		specs = append(specs, s)
		if !s.Removed {
			byIP[s.IP] = s
		}
	}
	legacy := map[string]EgressSpec{}
	for _, s := range static {
		if dyn, ok := byIP[s.IP]; ok {
			legacy[s.ProxyURL] = dyn // same exit IP, served by the agent now
			continue
		}
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	if changed {
		r.saveEgressRegistry(ctx)
	}
	d.specs, d.legacy, d.at = specs, legacy, time.Now()
	return specs, legacy
}

// AckEgressPublicIP accepts an egress's current public IP as the expected one.
func (r *Runtime) AckEgressPublicIP(ctx context.Context, id string) error {
	d := &r.egress.disc
	d.mu.Lock()
	defer d.mu.Unlock()
	r.loadEgressRegistry(ctx)
	e := d.registry[id]
	if e == nil {
		return fmt.Errorf("unknown egress %q", id)
	}
	e.ExpectedPublic = e.Spec.PublicIP
	e.Spec.PublicChanged, e.Spec.ExpectedPublicIP = false, ""
	r.saveEgressRegistry(ctx)
	d.at = time.Time{}
	return nil
}

// ForgetEgress drops a removed egress from the registry (no credentials may use it).
func (r *Runtime) ForgetEgress(ctx context.Context, id string) error {
	d := &r.egress.disc
	d.mu.Lock()
	defer d.mu.Unlock()
	r.loadEgressRegistry(ctx)
	e := d.registry[id]
	if e == nil {
		return fmt.Errorf("unknown egress %q", id)
	}
	if !e.Spec.Removed {
		return fmt.Errorf("egress %q is still present on its node", id)
	}
	delete(d.registry, id)
	r.saveEgressRegistry(ctx)
	d.at = time.Time{}
	return nil
}
