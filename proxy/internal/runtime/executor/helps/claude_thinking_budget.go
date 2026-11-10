package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeThinkingHeadroom is the smallest gap that satisfies Anthropic's
// strict requirement that max output tokens be greater than the thinking budget.
const claudeThinkingHeadroom int64 = 1

const (
	antigravityMaxOutputTokensPath = "request.generationConfig.maxOutputTokens"
	antigravityThinkingBudgetPath  = "request.generationConfig.thinkingConfig.thinkingBudget"
)

// ReconcileAntigravityClaudeThinkingBudget makes a Claude thinking budget
// strictly smaller than maxOutputTokens after every max-token cap has been applied.
//
// Thinking normalization runs earlier, against the max that exists at that
// moment. Antigravity then caps maxOutputTokens to the model's completion
// limit (64000 for claude-opus-4-6-thinking, while the official Claude model
// allows 128000). A client max above that cap with a budget equal to the cap
// becomes budget == max, which Vertex-wrapped Anthropic rejects with
// `max_tokens` must be greater than `thinking.budget_tokens`.
//
// Already-valid requests are returned unchanged. When budget >= max:
//   - keep the budget and raise max to budget+1 when that stays within the
//     model's completion limit (the output ceiling only moves up, so the
//     client's intended output is not reduced);
//   - otherwise lower the budget to max-1 when that stays at or above
//     Thinking.Min (the observed 64000/64000 case becomes budget 63999,
//     max 64000);
//   - if max-1 would fall below Thinking.Min, leave the body unchanged and
//     do not remove thinking. Dropping it would change the request more than
//     the upstream rejection. The Claude applier keeps the same invalid pair.
func ReconcileAntigravityClaudeThinkingBudget(payload []byte, modelName string) []byte {
	if !strings.Contains(strings.ToLower(modelName), "claude") {
		return payload
	}
	budgetResult := gjson.GetBytes(payload, antigravityThinkingBudgetPath)
	if !budgetResult.Exists() || budgetResult.Type != gjson.Number {
		return payload
	}
	budget := budgetResult.Int()
	// Non-positive budgets are dynamic (-1), disabled (0), or absent in effect.
	if budget <= 0 {
		return payload
	}
	maxResult := gjson.GetBytes(payload, antigravityMaxOutputTokensPath)
	if !maxResult.Exists() || maxResult.Type != gjson.Number {
		return payload
	}
	maxOut := maxResult.Int()
	if maxOut <= 0 || budget < maxOut {
		return payload
	}

	modelCap, minBudget := claudeThinkingLimits(modelName)
	// Raising max keeps the client's budget and does not shrink the output
	// ceiling. It is only possible while budget+1 is still inside the model cap.
	if modelCap > budget {
		raised := budget + claudeThinkingHeadroom
		if raised <= modelCap {
			return writeClaudeThinkingLimit(payload, modelName, antigravityMaxOutputTokensPath, raised, budget, budget, maxOut, raised)
		}
	}

	lowered := maxOut - claudeThinkingHeadroom
	if lowered > 0 && lowered >= minBudget {
		return writeClaudeThinkingLimit(payload, modelName, antigravityThinkingBudgetPath, lowered, budget, lowered, maxOut, maxOut)
	}

	// max <= Thinking.Min (or max-1 would). Keep the thinking block; do not
	// invent a budget below the model minimum and do not delete the config.
	log.WithFields(log.Fields{
		"model":                    modelName,
		"budget_before":            budget,
		"budget_after":             budget,
		"max_output_tokens_before": maxOut,
		"max_output_tokens_after":  maxOut,
		"thinking_min":             minBudget,
		"model_max_completion":     modelCap,
	}).Debug("antigravity: left claude thinking budget >= max output tokens; max-1 is below the model minimum and max cannot be raised")
	return payload
}

func claudeThinkingLimits(modelName string) (modelCap, minBudget int64) {
	info := registry.LookupModelInfo(modelName, "antigravity")
	if info == nil {
		return 0, 0
	}
	if info.MaxCompletionTokens > 0 {
		modelCap = int64(info.MaxCompletionTokens)
	}
	if info.Thinking != nil && info.Thinking.Min > 0 {
		minBudget = int64(info.Thinking.Min)
	}
	return modelCap, minBudget
}

func writeClaudeThinkingLimit(payload []byte, modelName, path string, value, budgetBefore, budgetAfter, maxBefore, maxAfter int64) []byte {
	updated, err := sjson.SetBytes(payload, path, value)
	if err != nil {
		log.WithFields(log.Fields{
			"model": modelName,
			"path":  path,
			"error": err.Error(),
		}).Debug("antigravity: failed to reconcile claude thinking budget")
		return payload
	}
	log.WithFields(log.Fields{
		"model":                    modelName,
		"budget_before":            budgetBefore,
		"budget_after":             budgetAfter,
		"max_output_tokens_before": maxBefore,
		"max_output_tokens_after":  maxAfter,
	}).Debug("antigravity: reconciled claude thinking budget to stay below max output tokens")
	return updated
}
