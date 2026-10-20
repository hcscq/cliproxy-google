package home

import (
	"testing"
	"time"

	config "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClusterHealthSettingsOverlay(t *testing.T) {
	s := clusterHealthSettings(config.AntigravityClusterHealthConfig{})
	if s.DemoteTTFBP95 != 30*time.Second || s.DemoteFail != 0.4 || s.MaxDegradedFrac != 0.3 || s.ProbeFloor != 0.15 {
		t.Fatalf("defaults: %+v", s)
	}
	s = clusterHealthSettings(config.AntigravityClusterHealthConfig{DemoteTTFBP95Seconds: -1, DemoteFailPercent: 55, MaxDemotedPercent: 20, ProbeFloorPercent: 10, QuarantineSeconds: 600, QuarantineMaxSeconds: 60})
	if s.DemoteTTFBP95 != 0 || s.DemoteFail != 0.55 || s.MaxDegradedFrac != 0.2 || s.ProbeFloor != 0.1 || s.QuarantineMax != 600*time.Second {
		t.Fatalf("overlay: %+v", s)
	}
}

func TestClusterHealthStallCooldownSetting(t *testing.T) {
	if s := clusterHealthSettings(config.AntigravityClusterHealthConfig{}); s.SubThresholdCool != 45*time.Second {
		t.Fatalf("default stall cooldown %s", s.SubThresholdCool)
	}
	if s := clusterHealthSettings(config.AntigravityClusterHealthConfig{StallCooldownSeconds: 30}); s.SubThresholdCool != 30*time.Second {
		t.Fatalf("panel 30: %s", s.SubThresholdCool)
	}
	if s := clusterHealthSettings(config.AntigravityClusterHealthConfig{StallCooldownSeconds: 1}); s.SubThresholdCool != 5*time.Second {
		t.Fatalf("clamp low: %s", s.SubThresholdCool)
	}
	if s := clusterHealthSettings(config.AntigravityClusterHealthConfig{StallCooldownSeconds: 99999}); s.SubThresholdCool != 600*time.Second {
		t.Fatalf("clamp high: %s", s.SubThresholdCool)
	}
}
