package thinking

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// ExactThinkingParams describes the thinking parameters actually present in a
// translated upstream request body, e.g. "level=low", "budget=8192",
// "budget=0", "budget=-1", "disabled", "effort=high" ("" when none are sent).
// Unlike ExtractTranslatedReasoningEffort it never collapses a budget to a level
// label, so the request log shows what the upstream received.
func ExactThinkingParams(body []byte, format string) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	var parts []string
	for _, prefix := range []string{"request.generationConfig.thinkingConfig", "generationConfig.thinkingConfig", "generation_config.thinking_config"} {
		tc := gjson.GetBytes(body, prefix)
		if !tc.Exists() {
			continue
		}
		if v := firstExisting(tc, "thinkingLevel", "thinking_level"); v.Exists() {
			parts = append(parts, "level="+strings.ToLower(v.String()))
		}
		if v := firstExisting(tc, "thinkingBudget", "thinking_budget"); v.Exists() {
			parts = append(parts, fmt.Sprintf("budget=%d", v.Int()))
		}
		if v := firstExisting(tc, "includeThoughts", "include_thoughts"); v.Exists() {
			parts = append(parts, fmt.Sprintf("include_thoughts=%t", v.Bool()))
		}
		return strings.Join(parts, ",")
	}
	if t := gjson.GetBytes(body, "thinking"); t.Exists() && t.IsObject() {
		switch typ := strings.ToLower(t.Get("type").String()); typ {
		case "disabled":
			parts = append(parts, "disabled")
		case "":
		default:
			if b := t.Get("budget_tokens"); b.Exists() {
				parts = append(parts, fmt.Sprintf("budget=%d", b.Int()))
			} else {
				parts = append(parts, typ)
			}
		}
	}
	for _, path := range []string{"output_config.effort", "reasoning_effort", "reasoning.effort"} {
		if v := gjson.GetBytes(body, path); v.Exists() && v.String() != "" {
			parts = append(parts, "effort="+strings.ToLower(v.String()))
			break
		}
	}
	if len(parts) == 0 {
		return ExtractTranslatedReasoningEffort(body, format)
	}
	return strings.Join(parts, ",")
}

func firstExisting(r gjson.Result, keys ...string) gjson.Result {
	for _, k := range keys {
		if v := r.Get(k); v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

// OffAdjustmentNote explains, for a request that asked to turn thinking off,
// what was sent instead when the model cannot fully disable thinking. It
// returns "" when thinking was not requested off or was really disabled.
func OffAdjustmentNote(requestedEffort, sent string, support *registry.ThinkingSupport) string {
	if !strings.EqualFold(strings.TrimSpace(requestedEffort), string(LevelNone)) {
		return ""
	}
	offMode := ""
	if support != nil {
		offMode = support.OffMode
	}
	switch offMode {
	case OffModeZero, OffModeOmit:
		return ""
	case OffModeZeroReduced:
		return "thinking cannot be fully disabled on this model; sent budget=0 (upstream minimum, still thinks briefly)"
	}
	if sent == "" || sent == "disabled" || strings.HasPrefix(sent, "budget=0") {
		return ""
	}
	return "thinking cannot be disabled on this model; sent " + sent + " instead"
}
