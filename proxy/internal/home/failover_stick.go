package home

import (
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Env-tuned failover stickiness. Defaults keep a proxy on the home it failed
// over TO long enough that CLUSTER NODES "assigned" cannot yank it back to the
// unhealthy home that just timed out (2026-11-02 incident: heartbeat failover to v8pgr then refreshBestClusterNode pulled back to 7lc9j within ~1s).
var (
	homeFailoverStickCooldown = envDuration("HOME_FAILOVER_STICK_COOLDOWN", 2*time.Minute)
	// Consecutive home_unavailable (RPop/heartbeat path) before forcing a
	// switch to another live home. 0 disables.
	homeUnavailableForceSwitch = envInt("HOME_UNAVAILABLE_FORCE_SWITCH", 8)
)

func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && d >= 0 {
		return d
	}
	return def
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v >= 0 {
		return v
	}
	return def
}

type stickTarget struct {
	ip   string
	port int
}

func (t stickTarget) empty() bool { return t.ip == "" || t.port <= 0 }

func (t stickTarget) equal(ip string, port int) bool {
	return t.ip == strings.TrimSpace(ip) && t.port == port
}

func (t stickTarget) addr() string {
	if t.empty() {
		return ""
	}
	return netJoin(t.ip, t.port)
}

func netJoin(ip string, port int) string {
	return strings.TrimSpace(ip) + ":" + strconv.Itoa(port)
}

// stickActiveLocked reports whether failover stickiness is in force. Caller holds c.mu.
func (c *Client) stickActiveLocked() bool {
	if c == nil || homeFailoverStickCooldown <= 0 {
		return false
	}
	return !c.stickUntil.IsZero() && time.Now().Before(c.stickUntil)
}

func (c *Client) setStickLocked(prefer, avoid stickTarget) {
	if c == nil {
		return
	}
	if homeFailoverStickCooldown <= 0 {
		c.stickUntil = time.Time{}
		c.stickPrefer = stickTarget{}
		c.stickAvoid = stickTarget{}
		return
	}
	c.stickPrefer = prefer
	c.stickAvoid = avoid
	c.stickUntil = time.Now().Add(homeFailoverStickCooldown)
}

// hasAlternateHomeLocked is true when clusterNodes lists a live home other than current.
func (c *Client) hasAlternateHomeLocked() bool {
	if c == nil {
		return false
	}
	curIP := strings.TrimSpace(c.homeCfg.Host)
	curPort := c.homeCfg.Port
	for _, n := range c.clusterNodes {
		ip := strings.TrimSpace(n.IP)
		if ip == "" || n.Port <= 0 {
			continue
		}
		if ip == curIP && n.Port == curPort {
			continue
		}
		return true
	}
	seedIP := strings.TrimSpace(c.seedHost)
	if seedIP != "" && c.seedPort > 0 && !(seedIP == curIP && c.seedPort == curPort) {
		return true
	}
	return false
}

// pickStickHomeLocked chooses the home to stay on during stickiness.
// Prefer the post-failover home when it is still live; otherwise any live
// home that is not the avoided one; otherwise empty (caller falls through).
func (c *Client) pickStickHomeLocked(nodes []clusterNode) (stickTarget, bool) {
	if !c.stickActiveLocked() {
		return stickTarget{}, false
	}
	live := func(t stickTarget) bool {
		if t.empty() {
			return false
		}
		for _, n := range nodes {
			if t.equal(n.IP, n.Port) {
				return true
			}
		}
		return false
	}
	if live(c.stickPrefer) {
		return c.stickPrefer, true
	}
	for _, n := range nodes {
		t := stickTarget{ip: strings.TrimSpace(n.IP), port: n.Port}
		if t.empty() {
			continue
		}
		if !c.stickAvoid.empty() && c.stickAvoid.equal(t.ip, t.port) {
			continue
		}
		return t, true
	}
	return stickTarget{}, false
}

// NoteHomeUnavailable records a transport-level home_unavailable. After
// HOME_UNAVAILABLE_FORCE_SWITCH consecutive hits (and only when another live
// home exists and stickiness is not already pinning us), force a failover and
// start stick cooldown so assigned cannot yank us back.
func (c *Client) NoteHomeUnavailable() {
	if c == nil || homeUnavailableForceSwitch <= 0 {
		return
	}
	c.mu.Lock()
	c.unavailableStreak++
	if c.unavailableStreak < homeUnavailableForceSwitch || c.stickActiveLocked() || !c.hasAlternateHomeLocked() {
		c.mu.Unlock()
		return
	}
	from := stickTarget{ip: strings.TrimSpace(c.homeCfg.Host), port: c.homeCfg.Port}
	switched, _ := c.switchToNextNodeLocked()
	if !switched {
		c.mu.Unlock()
		return
	}
	to := stickTarget{ip: strings.TrimSpace(c.homeCfg.Host), port: c.homeCfg.Port}
	c.setStickLocked(to, from)
	c.unavailableStreak = 0
	c.reassignPending = false
	cooldown := homeFailoverStickCooldown
	c.mu.Unlock()
	// Log after releasing mu. The process-wide logrus hook calls HeartbeatOK
	// and, historically, Enabled took this same mutex, so logging here wedged
	// the goroutine that still held mu and every request waiting on it.
	log.Warnf("home_unavailable streak forced failover to %s (stick %s, avoid %s)", to.addr(), cooldown, from.addr())
}

// NoteHomeAvailable resets the unavailable streak after a successful dispatch.
func (c *Client) NoteHomeAvailable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.unavailableStreak = 0
	c.mu.Unlock()
}

// HasAlternateHome reports whether a live alternate home exists (for readyz).
// TryLock keeps a stuck mu from hanging the readiness probe; a busy lock
// reports no alternate so the longer ready grace stays in force.
func (c *Client) HasAlternateHome() bool {
	if c == nil {
		return false
	}
	if !c.mu.TryLock() {
		return false
	}
	defer c.mu.Unlock()
	return c.hasAlternateHomeLocked()
}
