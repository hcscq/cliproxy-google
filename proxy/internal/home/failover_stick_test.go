package home

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// 2026-11-02: heartbeat failover switched to the healthy home, then refreshBestClusterNode (CLUSTER NODES + assigned) yanked the proxy back
// to the unhealthy home within ~1s. Stickiness must block that pull-back.
func TestRefreshBestClusterNodeRespectsFailoverStick(t *testing.T) {
	prevCooldown := homeFailoverStickCooldown
	homeFailoverStickCooldown = time.Minute
	t.Cleanup(func() { homeFailoverStickCooldown = prevCooldown })

	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	c.instanceID = "seed"
	c.clusterNodes = []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}

	c.mu.Lock()
	from := stickTarget{ip: "10.0.0.1", port: 8327}
	if !c.switchToNodeLocked(clusterNode{IP: "10.0.0.2", Port: 8327}) {
		c.mu.Unlock()
		t.Fatal("expected switch to healthy home")
	}
	c.setStickLocked(stickTarget{ip: "10.0.0.2", port: 8327}, from)
	c.mu.Unlock()

	// Simulate CLUSTER NODES with assigned still pointing at the bad home.
	nodes := []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}
	assigned := &clusterNode{IP: "10.0.0.1", Port: 8327}
	c.mu.Lock()
	ordered := orderClusterNodes(nodes, assigned, c.instanceID)
	c.clusterNodes = ordered
	stick, ok := c.pickStickHomeLocked(ordered)
	if !ok || !stick.equal("10.0.0.2", 8327) {
		c.mu.Unlock()
		t.Fatalf("stick pick = %+v ok=%v, want 10.0.0.2", stick, ok)
	}
	_ = c.switchToNodeLocked(clusterNode{IP: stick.ip, Port: stick.port})
	host := c.homeCfg.Host
	c.mu.Unlock()
	if host != "10.0.0.2" {
		t.Fatalf("host = %s, want stick prefer 10.0.0.2 (must not bounce to assigned 10.0.0.1)", host)
	}
}

func TestAssignedPushSuppressedDuringStick(t *testing.T) {
	prevCooldown := homeFailoverStickCooldown
	homeFailoverStickCooldown = time.Minute
	t.Cleanup(func() { homeFailoverStickCooldown = prevCooldown })

	c := New(config.HomeConfig{Host: "10.0.0.2", Port: 8327})
	c.mu.Lock()
	c.setStickLocked(stickTarget{ip: "10.0.0.2", port: 8327}, stickTarget{ip: "10.0.0.1", port: 8327})
	c.mu.Unlock()

	if err := c.updateClusterNodesFromPayload([]byte(twoHomes + `,"assigned":{"ip":"10.0.0.1","port":8327}}`)); err != nil {
		t.Fatal(err)
	}
	if c.takeReassignPending() {
		t.Fatal("assigned pull-back to avoided home must be suppressed during stick")
	}
	if c.homeCfg.Host != "10.0.0.2" {
		t.Fatalf("host = %s, want 10.0.0.2", c.homeCfg.Host)
	}
}

func TestNoteHomeUnavailableForcesFailoverOnceThenSticks(t *testing.T) {
	prevCooldown := homeFailoverStickCooldown
	prevThresh := homeUnavailableForceSwitch
	homeFailoverStickCooldown = time.Minute
	homeUnavailableForceSwitch = 3
	t.Cleanup(func() {
		homeFailoverStickCooldown = prevCooldown
		homeUnavailableForceSwitch = prevThresh
	})

	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	c.clusterNodes = []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}

	c.NoteHomeUnavailable()
	c.NoteHomeUnavailable()
	if c.homeCfg.Host != "10.0.0.1" {
		t.Fatal("must not switch before threshold")
	}
	c.NoteHomeUnavailable() // 3rd → force
	if c.homeCfg.Host != "10.0.0.2" {
		t.Fatalf("host = %s after force, want 10.0.0.2", c.homeCfg.Host)
	}

	// Further unavailable while stuck must not ping-pong back.
	for i := 0; i < 10; i++ {
		c.NoteHomeUnavailable()
	}
	if c.homeCfg.Host != "10.0.0.2" {
		t.Fatalf("ping-pong: host = %s, want stay on 10.0.0.2 during stick", c.homeCfg.Host)
	}
}

func TestNoteHomeUnavailableNoAlternateDoesNotSwitch(t *testing.T) {
	prevThresh := homeUnavailableForceSwitch
	homeUnavailableForceSwitch = 1
	t.Cleanup(func() { homeUnavailableForceSwitch = prevThresh })

	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	c.seedHost, c.seedPort = "10.0.0.1", 8327
	c.clusterNodes = []clusterNode{{IP: "10.0.0.1", Port: 8327}}
	c.NoteHomeUnavailable()
	if c.homeCfg.Host != "10.0.0.1" {
		t.Fatal("must not switch when no alternate home exists")
	}
}

func TestNoteHomeAvailableResetsStreak(t *testing.T) {
	prevThresh := homeUnavailableForceSwitch
	homeUnavailableForceSwitch = 3
	t.Cleanup(func() { homeUnavailableForceSwitch = prevThresh })

	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	c.clusterNodes = []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}
	c.NoteHomeUnavailable()
	c.NoteHomeUnavailable()
	c.NoteHomeAvailable()
	c.NoteHomeUnavailable()
	c.NoteHomeUnavailable()
	if c.homeCfg.Host != "10.0.0.1" {
		t.Fatal("streak reset should require threshold again before force switch")
	}
}

func TestSwitchToNextSetsStick(t *testing.T) {
	prevCooldown := homeFailoverStickCooldown
	homeFailoverStickCooldown = time.Minute
	t.Cleanup(func() { homeFailoverStickCooldown = prevCooldown })

	c := New(config.HomeConfig{Host: "10.0.0.1", Port: 8327})
	c.clusterNodes = []clusterNode{{IP: "10.0.0.1", Port: 8327}, {IP: "10.0.0.2", Port: 8327}}
	c.mu.Lock()
	switched, addr := c.switchToNextNodeLocked()
	active := c.stickActiveLocked()
	avoid := c.stickAvoid
	prefer := c.stickPrefer
	c.mu.Unlock()
	if !switched || addr != "10.0.0.2:8327" {
		t.Fatalf("switch = %v %s", switched, addr)
	}
	if !active || !prefer.equal("10.0.0.2", 8327) || !avoid.equal("10.0.0.1", 8327) {
		t.Fatalf("stick prefer=%+v avoid=%+v active=%v", prefer, avoid, active)
	}
}
