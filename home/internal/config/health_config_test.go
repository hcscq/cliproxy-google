package config

import "testing"

func TestV8ConfigAcceptsAntigravityHealth(t *testing.T) {
	data := []byte(`config-version: 8
oauth:
  providers:
    antigravity:
      health:
        ttfb-timeout-by-family: "3.8=28"
        cluster:
          demote-ttfb-p95-seconds: 30
          demote-fail-percent: 40
          max-demoted-percent: 30
          probe-floor-percent: 15
`)
	if err := ValidateV8Config(data); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
