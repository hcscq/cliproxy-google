package modelconfig

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

// NormalizeThinkingSupport clones and normalizes configured reasoning levels.
func NormalizeThinkingSupport(raw *registry.ThinkingSupport) *registry.ThinkingSupport {
	if raw == nil {
		return nil
	}
	normalized := *raw
	normalized.Levels = nil
	seen := make(map[string]struct{}, len(raw.Levels))
	for _, value := range raw.Levels {
		level := strings.ToLower(strings.TrimSpace(value))
		if level == "" {
			continue
		}
		switch level {
		case "none":
			normalized.ZeroAllowed = true
		case "auto":
			normalized.DynamicAllowed = true
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		normalized.Levels = append(normalized.Levels, level)
	}
	return &normalized
}

// ApplyConfiguredCapabilities applies explicit overrides without changing legacy defaults.
func ApplyConfiguredCapabilities(info *registry.ModelInfo, model any) {
	if entry, ok := model.(interface {
		GetThinking() *registry.ThinkingSupport
	}); ok && entry.GetThinking() != nil {
		info.Thinking = NormalizeThinkingSupport(entry.GetThinking())
		info.UserDefined = false
	}
	if entry, ok := model.(interface{ GetMaxContextLength() int }); ok && entry.GetMaxContextLength() > 0 {
		info.ContextLength = entry.GetMaxContextLength()
	}
}
