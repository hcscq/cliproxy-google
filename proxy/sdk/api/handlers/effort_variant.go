package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
)

var effortVariantLevels = []string{"low", "medium", "high"}

// splitEffortVariant splits "claude-opus-5-5-high" into ("claude-opus-5-5", "high").
func splitEffortVariant(model string) (string, string, bool) {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, level := range effortVariantLevels {
		if strings.HasSuffix(lower, "-"+level) {
			return model[:len(model)-len(level)-1], level, true
		}
	}
	return "", "", false
}

// effortVariantLevel maps a canonical reasoning effort to the upstream
// variant tier. Efforts above the top tier use the top tier; minimal uses low
// (the lowest tier, still at least what was asked). none/auto/unknown => "".
func effortVariantLevel(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	}
	return ""
}

// resolveEffortVariantModel lets a standard model name that is an Antigravity
// alias of an effort variant (e.g. claude-opus-5-5 -> claude-opus-5-5-high,
// configured in the panel's model aliases) pick the sibling variant matching
// the request's reasoning effort (reasoning_effort, output_config.effort,
// thinking budget, or a "(level)" suffix). Without an effort the alias target
// (the panel default) is used unchanged. The model family never changes.
func (h *BaseAPIHandler) resolveEffortVariantModel(entryProtocol, modelName string, rawJSON []byte) string {
	if h == nil || h.AuthManager == nil {
		return modelName
	}
	base := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if base == "" {
		base = strings.TrimSpace(modelName)
	}
	target := h.AuthManager.OAuthAliasUpstream("antigravity", base)
	if target == "" {
		return modelName
	}
	family, current, ok := splitEffortVariant(target)
	if !ok {
		return modelName
	}
	effort := thinking.ExtractReasoningEffort(rawJSON, entryProtocol, modelName)
	if effort == "" {
		// Anthropic Messages effort (output_config.effort) / Responses reasoning.effort.
		for _, path := range []string{"output_config.effort", "reasoning.effort"} {
			if v := gjson.GetBytes(rawJSON, path); v.Exists() {
				effort = v.String()
				break
			}
		}
	}
	level := effortVariantLevel(effort)
	if level == "" || level == current {
		return modelName
	}
	return family + "-" + level
}
