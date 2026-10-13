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
	if cfg == nil || cfg.Antigravity.PromptRewrites == nil {
		return config.DefaultAntigravityPromptRewrites()
	}
	return *cfg.Antigravity.PromptRewrites
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
				rewrites := r.RemoveLine || r.Replace != ""
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
				case r.Replace != "":
					updated = strings.ReplaceAll(updated, r.Match, r.Replace)
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
