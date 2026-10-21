package config

import (
	"testing"
	"time"
)

func TestReserveBanProbeSettings(t *testing.T) {
	var c Config
	s := c.ReserveSettings()
	if s.BanProbeEvery != 12*time.Hour || !s.PrePromoteCheck {
		t.Fatalf("defaults: %+v", s)
	}
	off := false
	c.ReserveBanProbeInterval, c.ReservePrePromoteCheck = "0", &off
	if s = c.ReserveSettings(); s.BanProbeEvery != 0 || s.PrePromoteCheck {
		t.Fatalf("off: %+v", s)
	}
	c.ReserveBanProbeInterval = "1m"
	if s = c.ReserveSettings(); s.BanProbeEvery != 10*time.Minute {
		t.Fatalf("clamp: %+v", s)
	}
}
