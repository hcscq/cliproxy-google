package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// builtinOAuthModelAliases are default per-channel aliases applied after the
// operator's oauth-model-alias config (a configured alias with the same name
// wins). They only name the same upstream model under a shorter ID, never a
// different model. With Fork, the alias is listed only for credentials whose
// catalog contains the upstream model, alongside the original ID.
var builtinOAuthModelAliases = map[string][]internalconfig.OAuthModelAlias{
	// Bare "gemini-4-argon" is not an upstream ID (404); the Antigravity client and
	// sub2api treat it as the High variant.
	"antigravity": {{Name: "gemini-4-argon-high", Alias: "gemini-4-argon", Fork: true}},
}

// WithBuiltinOAuthModelAliases returns aliases with the built-in defaults
// appended per channel, skipping any alias name the input already defines.
// The input map is not modified.
func WithBuiltinOAuthModelAliases(aliases map[string][]internalconfig.OAuthModelAlias) map[string][]internalconfig.OAuthModelAlias {
	out := make(map[string][]internalconfig.OAuthModelAlias, len(aliases)+len(builtinOAuthModelAliases))
	for channel, entries := range aliases {
		out[channel] = entries
	}
	for channel, builtins := range builtinOAuthModelAliases {
		key := ""
		for existing := range out {
			if strings.EqualFold(strings.TrimSpace(existing), channel) {
				key = existing
				break
			}
		}
		if key == "" {
			key = channel
		}
		current := out[key]
		seen := make(map[string]struct{}, len(current))
		for _, entry := range current {
			seen[strings.ToLower(strings.TrimSpace(entry.Alias))] = struct{}{}
		}
		merged := append([]internalconfig.OAuthModelAlias(nil), current...)
		for _, entry := range builtins {
			if _, exists := seen[strings.ToLower(entry.Alias)]; exists {
				continue
			}
			merged = append(merged, entry)
		}
		out[key] = merged
	}
	return out
}
