package config

import (
	"path"
	"strings"
)

// DefaultUnavailableModels is used when health.unavailable-models is unset:
// models the upstream lists but never serves (gemini-2.5-pro answers every
// credential with MODEL_CAPACITY_EXHAUSTED / per-model 429).
const DefaultUnavailableModels = "gemini-2.5-pro"

// ParseUnavailableModels splits a model-unavailable list (comma, semicolon or
// newline separated, case-insensitive, '*' wildcards). nil = default list;
// "" or "off" = none.
func ParseUnavailableModels(spec *string) []string {
	raw := DefaultUnavailableModels
	if spec != nil {
		raw = *spec
	}
	if strings.EqualFold(strings.TrimSpace(raw), "off") {
		return nil
	}
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// NormalizeUnavailableModel strips a "models/" prefix and a thinking suffix
// such as "(8192)" so "gemini-2.5-pro(8192)" matches "gemini-2.5-pro".
func NormalizeUnavailableModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if i := strings.IndexByte(m, '('); i > 0 && strings.HasSuffix(m, ")") {
		m = m[:i]
	}
	return strings.TrimSpace(m)
}

// MatchUnavailableModel reports whether model matches one of patterns.
func MatchUnavailableModel(patterns []string, model string) bool {
	m := NormalizeUnavailableModel(model)
	if m == "" {
		return false
	}
	for _, p := range patterns {
		if p == m {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := path.Match(p, m); ok {
				return true
			}
		}
	}
	return false
}

// ModelUnavailable reports whether model is on this config's unavailable list.
func (h AntigravityHealthConfig) ModelUnavailable(model string) bool {
	return MatchUnavailableModel(ParseUnavailableModels(h.UnavailableModels), model)
}
