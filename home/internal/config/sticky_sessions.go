package config

import (
	"strings"
	"time"
)

// StickySessionsConfig configures credential affinity (routing.sticky-sessions).
type StickySessionsConfig struct {
	// Enabled turns sticky routing on. nil = true.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// TTL is how long an idle binding is kept (Go duration, default 45m).
	TTL string `yaml:"ttl,omitempty" json:"ttl,omitempty"`
	// KeySources lists the enabled affinity key sources in priority order:
	// header, prompt-cache-key, openai-user, claude-metadata,
	// gemini-cached-content, prefix. Empty = all.
	KeySources []string `yaml:"key-sources,omitempty" json:"key-sources,omitempty"`
	// PrefixMessages is how many leading non-system messages the prompt-prefix
	// key covers (default 1, so a continued conversation keeps its key).
	PrefixMessages int `yaml:"prefix-messages,omitempty" json:"prefix-messages,omitempty"`
	// PrefixChars caps the prefix text hashed (default 8000 chars ≈ 2k tokens).
	PrefixChars int `yaml:"prefix-chars,omitempty" json:"prefix-chars,omitempty"`
	// MinPrefixChars: shorter prompts get no prefix key, so tiny identical
	// prompts from many callers are not all pinned to one credential
	// (default 2000; implicit caching needs ~1k+ tokens anyway).
	MinPrefixChars int `yaml:"min-prefix-chars,omitempty" json:"min-prefix-chars,omitempty"`
}

// StickyEnabled reports whether sticky routing is on (default true).
func (c StickySessionsConfig) StickyEnabled() bool { return c.Enabled == nil || *c.Enabled }

// StickyTTL returns the binding TTL (default 45m, clamped to [1m, 24h]).
func (c StickySessionsConfig) StickyTTL() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.TTL))
	if err != nil || d <= 0 {
		return 45 * time.Minute
	}
	if d < time.Minute {
		d = time.Minute
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return d
}
