package helps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

const claudeOpusThinkingModel = "claude-opus-4-6-thinking"

func TestReconcileAntigravityClaudeThinkingBudgetEqualCap(t *testing.T) {
	info := registry.LookupModelInfo(claudeOpusThinkingModel, "antigravity")
	if info == nil || info.MaxCompletionTokens != 64000 || info.Thinking == nil || info.Thinking.Min != 1024 {
		t.Fatalf("catalog entry for %s = %+v, want max completion 64000 and thinking min 1024", claudeOpusThinkingModel, info)
	}

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"SECRET PROMPT"}]}],"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"thinkingBudget":64000}}}}`)
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	})

	out := ReconcileAntigravityClaudeThinkingBudget(payload, claudeOpusThinkingModel)
	if got := gjson.GetBytes(out, antigravityThinkingBudgetPath).Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999", got)
	}
	if got := gjson.GetBytes(out, antigravityMaxOutputTokensPath).Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000", got)
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() == false {
		t.Fatal("thinking config was removed")
	}
	assertReconcileLog(t, hook, claudeOpusThinkingModel, 64000, 63999, 64000, 64000)
	if strings.Contains(hook.LastEntry().Message, "SECRET") || fieldContainsPrompt(hook.LastEntry()) {
		t.Fatalf("reconcile log included request content: %+v", hook.LastEntry().Data)
	}

	again := ReconcileAntigravityClaudeThinkingBudget(out, claudeOpusThinkingModel)
	if !bytes.Equal(again, out) {
		t.Fatalf("second pass changed an already valid body\n got %s\nwant %s", again, out)
	}
}

func TestReconcileAntigravityClaudeThinkingBudgetAlreadyValid(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"thinkingBudget":8191}}}}`)
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	})

	out := ReconcileAntigravityClaudeThinkingBudget(payload, claudeOpusThinkingModel)
	if !bytes.Equal(out, payload) {
		t.Fatalf("valid request was rewritten\n got %s\nwant %s", out, payload)
	}
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("valid request logged a reconcile: %+v", hook.LastEntry())
	}
}

func TestReconcileAntigravityClaudeThinkingBudgetRaisesMaxWithinCap(t *testing.T) {
	// Budget is below the model cap, so keep it and give the answer one token.
	payload := []byte(`{"request":{"generationConfig":{"maxOutputTokens":2000,"thinkingConfig":{"thinkingBudget":2000}}}}`)
	out := ReconcileAntigravityClaudeThinkingBudget(payload, claudeOpusThinkingModel)
	if got := gjson.GetBytes(out, antigravityThinkingBudgetPath).Int(); got != 2000 {
		t.Fatalf("thinkingBudget = %d, want 2000", got)
	}
	if got := gjson.GetBytes(out, antigravityMaxOutputTokensPath).Int(); got != 2001 {
		t.Fatalf("maxOutputTokens = %d, want 2001", got)
	}
}

func TestReconcileAntigravityClaudeThinkingBudgetKeepsThinkingWhenMaxAtMinimum(t *testing.T) {
	// max-1 is below Thinking.Min (1024) and budget+1 exceeds the model cap,
	// so neither adjustment is possible. Thinking must stay.
	payload := []byte(`{"request":{"generationConfig":{"maxOutputTokens":1000,"thinkingConfig":{"thinkingBudget":64000,"includeThoughts":true}}}}`)
	out := ReconcileAntigravityClaudeThinkingBudget(payload, claudeOpusThinkingModel)
	if !bytes.Equal(out, payload) {
		t.Fatalf("unfixable request was rewritten\n got %s\nwant %s", out, payload)
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatal("thinking config was removed")
	}
}

func TestReconcileAntigravityClaudeThinkingBudgetIgnoresNonClaudeAndDynamic(t *testing.T) {
	equal := []byte(`{"request":{"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"thinkingBudget":64000}}}}`)
	if out := ReconcileAntigravityClaudeThinkingBudget(equal, "gemini-3-flash"); !bytes.Equal(out, equal) {
		t.Fatalf("non-claude model was rewritten: %s", out)
	}
	dynamic := []byte(`{"request":{"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"thinkingBudget":-1}}}}`)
	if out := ReconcileAntigravityClaudeThinkingBudget(dynamic, claudeOpusThinkingModel); !bytes.Equal(out, dynamic) {
		t.Fatalf("dynamic budget was rewritten: %s", out)
	}
}

func assertReconcileLog(t *testing.T, hook *logtest.Hook, model string, budgetBefore, budgetAfter, maxBefore, maxAfter int64) {
	t.Helper()
	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("expected a debug log for the reconcile")
	}
	if entry.Level != log.DebugLevel {
		t.Fatalf("log level = %s, want debug", entry.Level)
	}
	if entry.Data["model"] != model {
		t.Fatalf("log model = %v, want %s", entry.Data["model"], model)
	}
	if entry.Data["budget_before"] != budgetBefore || entry.Data["budget_after"] != budgetAfter {
		t.Fatalf("log budget = %v -> %v, want %d -> %d", entry.Data["budget_before"], entry.Data["budget_after"], budgetBefore, budgetAfter)
	}
	if entry.Data["max_output_tokens_before"] != maxBefore || entry.Data["max_output_tokens_after"] != maxAfter {
		t.Fatalf("log max = %v -> %v, want %d -> %d", entry.Data["max_output_tokens_before"], entry.Data["max_output_tokens_after"], maxBefore, maxAfter)
	}
}

func fieldContainsPrompt(entry *log.Entry) bool {
	if entry == nil {
		return false
	}
	for _, value := range entry.Data {
		text, ok := value.(string)
		if ok && strings.Contains(text, "SECRET") {
			return true
		}
	}
	return false
}
