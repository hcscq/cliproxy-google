// Package egressagent is the per-node egress proxy: an HTTP CONNECT proxy (port 443
// only) that binds each tunnel to one of the node's public IPv4 addresses. The
// caller selects the source IP with the proxy username
// (http://<source-ip>@<node>:<port>), so one listener serves every local IP and new
// IPs need no new ports or firewall rules. Local IPs are re-discovered periodically
// and each one's real public exit IP is probed; GET /inventory reports both to Home.
package egressagent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Config tunes the agent.
type Config struct {
	Node    string
	Listen  string          // e.g. 172.16.1.2:3141
	Exclude map[string]bool // local IPs never offered
	// ExcludeFile, when set, is re-read on every discovery (one IP per line or
	// comma-separated; # comments) so exclusions change without a restart.
	ExcludeFile   string
	AllowSources  []netip.Prefix // client CIDRs allowed to use the proxy
	DiscoverEvery time.Duration
	ProbeEvery    time.Duration
	ProbeURL      string
	DialTimeout   time.Duration
	IdleTimeout   time.Duration
	// ListAddrs returns local IPv4 addresses (tests override).
	ListAddrs func() ([]netip.Addr, error)
	// PrimaryAddr returns the default-route source address (tests override).
	PrimaryAddr func() (netip.Addr, error)
	// Usable filters candidate addresses (default: public IPv4 only).
	Usable func(netip.Addr) bool
	// Ports are the allowed CONNECT target ports (default 443).
	Ports map[string]bool
}

// IP is one offered source address.
type IP struct {
	IP         string    `json:"ip"`
	Primary    bool      `json:"primary"`
	PublicIP   string    `json:"public_ip,omitempty"`
	ProbedAt   time.Time `json:"probed_at,omitempty"`
	ProbeError string    `json:"probe_error,omitempty"`
	FirstSeen  time.Time `json:"first_seen"`
}

// Inventory is GET /inventory.
type Inventory struct {
	Node     string    `json:"node"`
	Listen   string    `json:"listen"`
	IPs      []IP      `json:"ips"`
	Excluded []string  `json:"excluded,omitempty"`
	At       time.Time `json:"at"`
}

// Agent is the running proxy.
type Agent struct {
	cfg      Config
	mu       sync.RWMutex
	ips      map[string]*IP
	excluded map[string]bool
}

// excludeSet merges static and file exclusions (a missing file means none).
func (a *Agent) excludeSet() map[string]bool {
	out := map[string]bool{}
	for k := range a.cfg.Exclude {
		out[k] = true
	}
	if a.cfg.ExcludeFile != "" {
		if b, err := os.ReadFile(a.cfg.ExcludeFile); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line, _, _ = strings.Cut(line, "#")
				for _, f := range strings.Split(line, ",") {
					if ip, err := netip.ParseAddr(strings.TrimSpace(f)); err == nil {
						out[ip.String()] = true
					}
				}
			}
		}
	}
	return out
}

// New builds an agent with defaults filled in.
func New(cfg Config) *Agent {
	if cfg.DiscoverEvery <= 0 {
		cfg.DiscoverEvery = 30 * time.Second
	}
	if cfg.ProbeEvery <= 0 {
		cfg.ProbeEvery = 5 * time.Minute
	}
	if cfg.ProbeURL == "" {
		cfg.ProbeURL = "https://api.ipify.org"
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 15 * time.Minute
	}
	if cfg.ListAddrs == nil {
		cfg.ListAddrs = systemAddrs
	}
	if cfg.PrimaryAddr == nil {
		cfg.PrimaryAddr = defaultRouteAddr
	}
	if cfg.Usable == nil {
		cfg.Usable = usable
	}
	if len(cfg.Ports) == 0 {
		cfg.Ports = map[string]bool{"443": true}
	}
	return &Agent{cfg: cfg, ips: map[string]*IP{}}
}

// usable reports whether a local address can be a public egress.
func usable(a netip.Addr) bool {
	if !a.Is4() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() {
		return false
	}
	if netip.MustParsePrefix("100.64.0.0/10").Contains(a) {
		return false
	}
	return true
}

func systemAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP.To4()); ok {
				out = append(out, ip)
			}
		}
	}
	return out, nil
}

func defaultRouteAddr() (netip.Addr, error) {
	c, err := net.Dial("udp4", "8.8.8.8:53") // no packet is sent
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	ap, err := netip.ParseAddrPort(c.LocalAddr().String())
	return ap.Addr(), err
}

// Discover refreshes the local IP set; it returns added and removed IPs.
func (a *Agent) Discover() (added, removed []string, err error) {
	list, err := a.cfg.ListAddrs()
	if err != nil {
		return nil, nil, err
	}
	primary, _ := a.cfg.PrimaryAddr()
	exclude := a.excludeSet()
	seen := map[string]bool{}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.excluded = exclude
	for _, ip := range list {
		if !a.cfg.Usable(ip) || exclude[ip.String()] {
			continue
		}
		s := ip.String()
		seen[s] = true
		cur := a.ips[s]
		if cur == nil {
			cur = &IP{IP: s, FirstSeen: time.Now().UTC()}
			a.ips[s] = cur
			added = append(added, s)
		}
		cur.Primary = primary.IsValid() && ip == primary
	}
	for s := range a.ips {
		if !seen[s] {
			delete(a.ips, s)
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// Probe measures each IP's public exit address (only stale ones unless force).
func (a *Agent) Probe(ctx context.Context, force bool) {
	a.mu.RLock()
	var todo []string
	for s, ip := range a.ips {
		if force || ip.ProbedAt.IsZero() || time.Since(ip.ProbedAt) >= a.cfg.ProbeEvery {
			todo = append(todo, s)
		}
	}
	a.mu.RUnlock()
	for _, s := range todo {
		pub, err := a.probeOne(ctx, s)
		a.mu.Lock()
		if ip := a.ips[s]; ip != nil {
			ip.ProbedAt = time.Now().UTC()
			if err != nil {
				ip.ProbeError = err.Error()
			} else {
				if ip.PublicIP != "" && ip.PublicIP != pub {
					log.Warnf("egress-agent: public IP of %s changed %s -> %s", s, ip.PublicIP, pub)
				}
				ip.PublicIP, ip.ProbeError = pub, ""
			}
		}
		a.mu.Unlock()
	}
}

func (a *Agent) probeOne(ctx context.Context, src string) (string, error) {
	d := &net.Dialer{Timeout: a.cfg.DialTimeout, LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}}
	tr := &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, a.cfg.ProbeURL, nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("probe: status %d %q", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return ip.String(), nil
}

// Snapshot returns the current inventory.
func (a *Agent) Snapshot() Inventory {
	a.mu.RLock()
	defer a.mu.RUnlock()
	inv := Inventory{Node: a.cfg.Node, Listen: a.cfg.Listen, At: time.Now().UTC()}
	for _, ip := range a.ips {
		inv.IPs = append(inv.IPs, *ip)
	}
	sort.Slice(inv.IPs, func(i, j int) bool { return inv.IPs[i].IP < inv.IPs[j].IP })
	for s := range a.excluded {
		inv.Excluded = append(inv.Excluded, s)
	}
	sort.Strings(inv.Excluded)
	return inv
}

func (a *Agent) has(ip string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ips[ip] != nil
}

// Run discovers/probes in the background and serves until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if _, _, err := a.Discover(); err != nil {
		return err
	}
	go a.Probe(ctx, true)
	go func() {
		t := time.NewTicker(a.cfg.DiscoverEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			added, removed, err := a.Discover()
			if err != nil {
				log.WithError(err).Warn("egress-agent: discover failed")
				continue
			}
			if len(added)+len(removed) > 0 {
				log.Infof("egress-agent: IPs added=%v removed=%v", added, removed)
			}
			a.Probe(ctx, false)
		}
	}()
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Infof("egress-agent %s listening on %s, IPs %v", a.cfg.Node, a.cfg.Listen, a.Snapshot().IPs)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *Agent) allowed(remote string) bool {
	if len(a.cfg.AllowSources) == 0 {
		return true
	}
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	for _, p := range a.cfg.AllowSources {
		if p.Contains(ap.Addr().Unmap()) {
			return true
		}
	}
	return false
}

// ServeHTTP handles CONNECT tunnels plus GET /healthz and /inventory.
func (a *Agent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.allowed(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodConnect {
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "ok\n")
		case "/inventory":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(a.Snapshot())
		default:
			http.Error(w, "CONNECT only", http.StatusForbidden)
		}
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || !a.cfg.Ports[port] || host == "" {
		http.Error(w, "CONNECT :443 only", http.StatusForbidden)
		return
	}
	src := proxyUser(r.Header.Get("Proxy-Authorization"))
	if src == "" {
		w.Header().Set("Proxy-Authenticate", `Basic realm="egress source ip"`)
		http.Error(w, "source IP required as proxy username", http.StatusProxyAuthRequired)
		return
	}
	if !a.has(src) {
		// Fail closed: an IP that is gone is never replaced by another one.
		http.Error(w, "source IP not available on this node", http.StatusServiceUnavailable)
		return
	}
	d := &net.Dialer{Timeout: a.cfg.DialTimeout, LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, KeepAlive: 30 * time.Second}
	upstream, err := d.DialContext(r.Context(), "tcp4", r.Host)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go a.splice(client, buf, upstream)
}

func (a *Agent) splice(client net.Conn, buf *bufio.ReadWriter, upstream net.Conn) {
	defer client.Close()
	defer upstream.Close()
	idle := a.cfg.IdleTimeout
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn, r io.Reader) {
		b := make([]byte, 32*1024)
		for {
			_ = src.SetReadDeadline(time.Now().Add(idle))
			n, err := r.Read(b)
			if n > 0 {
				if _, werr := dst.Write(b[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	var fromClient io.Reader = client
	if buf != nil && buf.Reader.Buffered() > 0 {
		fromClient = io.MultiReader(io.LimitReader(buf.Reader, int64(buf.Reader.Buffered())), client)
	}
	go cp(upstream, client, fromClient)
	go cp(client, upstream, upstream)
	<-done
	<-done
}

// proxyUser extracts the username of a Basic Proxy-Authorization header.
func proxyUser(h string) string {
	const prefix = "Basic "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return ""
	}
	user, _, _ := strings.Cut(string(raw), ":")
	if ip, err := netip.ParseAddr(strings.TrimSpace(user)); err == nil && ip.Is4() {
		return ip.String()
	}
	return ""
}
