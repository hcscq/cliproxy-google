package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func antigravityPromptRewriteRules(cfg *config.Config) []config.PromptRewriteRule {
	var base []config.PromptRewriteRule
	if cfg == nil || cfg.Antigravity.PromptRewrites == nil {
		base = config.DefaultAntigravityPromptRewrites()
	} else {
		base = *cfg.Antigravity.PromptRewrites
	}
	if cfg != nil && cfg.Antigravity.PromptRewritesExtra != nil && len(*cfg.Antigravity.PromptRewritesExtra) > 0 {
		out := make([]config.PromptRewriteRule, 0, len(base)+len(*cfg.Antigravity.PromptRewritesExtra))
		out = append(out, base...)
		out = append(out, (*cfg.Antigravity.PromptRewritesExtra)...)
		return out
	}
	return base
}

// cgj is U+034F (combining grapheme joiner): invisible, zero-width, and does
// not change the rendered text or its meaning.
const cgj = "\u034f"

// obfuscatedReplacement derives the replacement for an Obfuscate rule by
// inserting one cgj after the first rune of match. Returns "" when match has
// no rune to split on.
func obfuscatedReplacement(match string) string {
	for i := range match {
		if i == 0 {
			continue
		}
		return match[:i] + cgj + match[i:]
	}
	return ""
}

// ruleReplacement returns the effective replacement text for a rule: the
// explicit Replace, or (for an Obfuscate rule with no explicit Replace and no
// RemoveLine) the auto-derived U+034F form.
func ruleReplacement(r config.PromptRewriteRule) string {
	if r.Replace != "" {
		return r.Replace
	}
	if r.Obfuscate && !r.RemoveLine {
		return obfuscatedReplacement(r.Match)
	}
	return ""
}

// applyAntigravityPromptRewrites rewrites matching text in the system
// instruction. It returns the payload and the names of every rule that
// matched (rewritten or detect-only).
func applyAntigravityPromptRewrites(payload []byte, rules []config.PromptRewriteRule) ([]byte, []string, []string) {
	if len(rules) == 0 {
		return payload, nil, nil
	}
	var hits, detectOnly []string
	seen := map[string]bool{}
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		parts := gjson.GetBytes(payload, path+".parts")
		if !parts.IsArray() {
			continue
		}
		changed := false
		kept := []byte(`[]`)
		for _, part := range parts.Array() {
			text := part.Get("text")
			if text.Type != gjson.String {
				kept, _ = sjson.SetRawBytes(kept, "-1", []byte(part.Raw))
				continue
			}
			orig := text.String()
			updated := orig
			for _, r := range rules {
				if r.Disabled || r.Match == "" || !strings.Contains(updated, r.Match) {
					continue
				}
				name := r.Name
				if name == "" {
					name = r.Match
				}
				rep := ruleReplacement(r)
				rewrites := r.RemoveLine || rep != ""
				if !seen[name] {
					seen[name] = true
					hits = append(hits, name)
					if !rewrites {
						detectOnly = append(detectOnly, name)
					}
				}
				switch {
				case r.RemoveLine:
					lines := strings.Split(updated, "\n")
					out := lines[:0]
					for _, l := range lines {
						if !strings.Contains(l, r.Match) {
							out = append(out, l)
						}
					}
					updated = strings.Join(out, "\n")
				case rep != "":
					updated = strings.ReplaceAll(updated, r.Match, rep)
				}
			}
			if updated == orig {
				kept, _ = sjson.SetRawBytes(kept, "-1", []byte(part.Raw))
				continue
			}
			changed = true
			if strings.TrimSpace(updated) == "" {
				continue // drop parts that only held removed lines
			}
			np, _ := sjson.SetBytes([]byte(part.Raw), "text", updated)
			kept, _ = sjson.SetRawBytes(kept, "-1", np)
		}
		if !changed {
			continue
		}
		if gjson.ParseBytes(kept).Get("#").Int() == 0 {
			payload, _ = sjson.DeleteBytes(payload, path)
		} else {
			payload, _ = sjson.SetRawBytes(payload, path+".parts", kept)
		}
	}
	return payload, hits, detectOnly
}

// rewriteAntigravityPrompt applies the configured prompt rewrites, logs and
// records a request-log note when any rule matched, and returns the
// detect-only hits (text still present upstream) used for 429 classification.
func (e *AntigravityExecutor) rewriteAntigravityPrompt(ctx context.Context, payload []byte, reporter *helps.UsageReporter) ([]byte, []string) {
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	// Arm the per-request system-prompt capture when EITHER diagnostic is on:
	// the fingerprint sampler, or the opt-in full-context error capture. Both
	// self-gate downstream, so arming for one never feeds the other.
	if reporter != nil && (helps.SysPromptSampleEnabled() || helps.ErrorContextCaptureEnabled()) {
		reporter.ArmSysPromptSample(extractAntigravitySysPromptText(payload), countAntigravityToolDeclarations(payload))
	}
	out, hits, detectOnly := applyAntigravityPromptRewrites(payload, antigravityPromptRewriteRules(cfg))
	if len(hits) > 0 {
		label := "prompt_rewritten: "
		if len(detectOnly) == len(hits) {
			label = "prompt_flagged: "
		}
		if reporter != nil {
			reporter.SetPromptNote(label + strings.Join(hits, ","))
		}
		log.WithContext(ctx).Infof("antigravity executor: %s%s", label, strings.Join(hits, ","))
	}
	return out, detectOnly
}

// isAntigravityBare429 reports a 429 that carries no quota signal at all
// (no details, no reset hint). Antigravity returns exactly this for prompt
// pattern blocks; genuine quota/capacity 429s normally carry ErrorInfo or a
// reset time.
func isAntigravityBare429(body []byte) bool {
	e := gjson.GetBytes(body, "error")
	if !e.Exists() || e.Get("details").Exists() {
		return false
	}
	msg := strings.ToLower(e.Get("message").String())
	return !strings.Contains(msg, "reset") && !strings.Contains(msg, "quota reached") && !bytes.Contains(body, []byte("retryDelay"))
}

// antigravityPromptBlockErr converts a bare 429 on a request that matched a
// prompt rule into a request-scoped 400: the block follows the prompt, so
// rotating credentials or cooling them would only burn the pool.
func antigravityPromptBlockErr(hits []string) statusErr {
	msg := `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"upstream rejected this prompt (bare 429 after prompt pattern ` + strings.Join(hits, ",") + `); not retried on other credentials"}}`
	return statusErr{code: http.StatusBadRequest, msg: msg}
}

// extractAntigravitySysPromptText concatenates the text of every
// systemInstruction / system_instruction part (both camelCase and snake_case).
// It never reads user contents, tools or responses.
func extractAntigravitySysPromptText(payload []byte) string {
	var b strings.Builder
	for _, root := range []string{"request.systemInstruction", "request.system_instruction"} {
		gjson.GetBytes(payload, root+".parts").ForEach(func(_, part gjson.Result) bool {
			if t := part.Get("text"); t.Exists() {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(t.String())
			}
			return true
		})
	}
	return b.String()
}

// countAntigravityToolDeclarations returns the total number of function
// declarations across request.tools (a size signal only, never their content).
func countAntigravityToolDeclarations(payload []byte) int {
	n := 0
	gjson.GetBytes(payload, "request.tools").ForEach(func(_, tool gjson.Result) bool {
		n += len(tool.Get("functionDeclarations").Array())
		return true
	})
	return n
}
