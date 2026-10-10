package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Why upstream CLIProxyAPI strips maxOutputTokens for Gemini models on
// Antigravity: on Gemini, thinking tokens count against maxOutputTokens, so a
// small client limit (common in IDE clients) combined with a large thinking
// budget yields responses that spend the whole limit on thoughts and return no
// text (finishReason MAX_TOKENS, empty content), and Antigravity's own agent
// never sends a limit. Rather than ignoring the client limit we pass it through
// and keep room for the answer by lowering a fixed thinking budget.

// honorMaxTokens reports whether client output limits are passed upstream.
func honorMaxTokens(cfg *config.Config) bool {
	return cfg == nil || cfg.HonorMaxTokens == nil || *cfg.HonorMaxTokens
}

// fitGeminiMaxOutputTokens applies the honor-max-tokens policy to a Gemini
// payload whose generationConfig lives under prefix ("request." for
// Antigravity, "" for AI Studio). With the toggle off the limit is stripped
// (legacy behaviour). With it on, an absent limit stays absent; a present limit
// is kept and a fixed thinkingBudget >= limit is lowered to half the limit
// (respecting the model minimum; 0 when the model allows disabling thinking
// and the minimum does not fit). Level-based thinking (thinkingLevel) and
// dynamic budgets (-1) are left unchanged: their thoughts still count against
// the limit.
func fitGeminiMaxOutputTokens(cfg *config.Config, payload []byte, prefix, modelName string) []byte {
	maxPath := prefix + "generationConfig.maxOutputTokens"
	if !honorMaxTokens(cfg) {
		payload, _ = sjson.DeleteBytes(payload, maxPath)
		return payload
	}
	maxOut := gjson.GetBytes(payload, maxPath)
	if !maxOut.Exists() || maxOut.Type != gjson.Number {
		return payload
	}
	limit := int(maxOut.Int())
	if limit <= 0 {
		payload, _ = sjson.DeleteBytes(payload, maxPath)
		return payload
	}
	budgetPath := prefix + "generationConfig.thinkingConfig.thinkingBudget"
	budgetResult := gjson.GetBytes(payload, budgetPath)
	if !budgetResult.Exists() || budgetResult.Type != gjson.Number {
		return payload
	}
	budget := int(budgetResult.Int())
	if budget < 0 || budget < limit {
		return payload
	}
	minBudget, zeroAllowed := 0, false
	for _, provider := range []string{"antigravity", "gemini", ""} {
		if info := registry.LookupModelInfo(modelName, provider); info != nil && info.Thinking != nil {
			minBudget, zeroAllowed = info.Thinking.Min, info.Thinking.ZeroAllowed
			break
		}
	}
	newBudget := limit / 2
	if newBudget < minBudget {
		if zeroAllowed {
			newBudget = 0
		} else {
			newBudget = minBudget
		}
	}
	payload, _ = sjson.SetBytes(payload, budgetPath, newBudget)
	return payload
}
