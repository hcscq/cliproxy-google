package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Built-in tools + function declarations ("builtin-tools-with-functions").
//
// Antigravity rejects googleSearch/urlContext/codeExecution mixed with
// functionDeclarations (400 "Please enable tool_config.include_server_side_
// tool_invocations", toolConfig is ignored). "emulate" (default) keeps both
// capabilities with two upstream calls on the SAME model:
//
//	pass 1: googleSearch/urlContext only, non-stream, history folded to text
//	        (only when the last turn is user text)
//	pass 2: function declarations only, pass-1 findings + sources injected
//	        into the last user turn; streamed or not as the client asked.
//
// Pass-1 groundingMetadata/urlContextMetadata is attached to the final
// response and its usage is added to the response usageMetadata, so the
// client and the usage reporter both see the cost of both passes.
// codeExecution cannot be pre-run meaningfully, so requests containing it are
// passed through unchanged (as "keep").

const (
	builtinToolsModeEmulate = "emulate"
	builtinToolsModeKeep    = "keep"
	builtinToolsModeDrop    = "drop-builtin"

	defaultBuiltinPresearchTimeout = 20 * time.Second
	builtinPresearchFoldMax        = 2000
	antigravityBuiltinToolsHeader  = "X-Cpa-Builtin-Tools"
)

const builtinPresearchInstruction = "[Search step] Use Google Search (and URL context when URLs are given) to gather current, factual information needed to answer the user's latest message. Function tools mentioned in this conversation are not available in this step; do not try to call them. Reply only with a concise factual summary of the findings, including concrete facts, numbers and dates."

func antigravityBuiltinToolsMode() string {
	if live := antigravityHealthLive.Load(); live != nil && live.BuiltinToolsWithFunctions != nil {
		switch v := strings.ToLower(strings.TrimSpace(*live.BuiltinToolsWithFunctions)); v {
		case builtinToolsModeKeep, builtinToolsModeDrop:
			return v
		}
	}
	return builtinToolsModeEmulate
}

func antigravityBuiltinPresearchTimeout(attemptLimit time.Duration) time.Duration {
	t := defaultBuiltinPresearchTimeout
	if live := antigravityHealthLive.Load(); live != nil && live.BuiltinPresearchTimeoutSeconds > 0 {
		t = time.Duration(live.BuiltinPresearchTimeoutSeconds) * time.Second
	}
	// Leave pass 2 at least half of the first-byte / attempt deadline.
	if attemptLimit > 0 && t > attemptLimit/2 {
		t = attemptLimit / 2
	}
	return t
}

func isGeminiSearchToolKey(k string) bool {
	switch k {
	case "googleSearch", "google_search", "googleSearchRetrieval", "google_search_retrieval", "urlContext", "url_context":
		return true
	}
	return false
}

func isGeminiCodeExecutionToolKey(k string) bool { return k == "codeExecution" || k == "code_execution" }

// splitGeminiTools separates a tools array into function-only entries and the
// pass-1 built-in tools (normalized to camelCase; retrieval -> googleSearch).
func splitGeminiTools(tools gjson.Result) (fnEntries []string, searchTools []string, hasFn, hasSearch, hasCode bool) {
	seen := map[string]bool{}
	for _, t := range tools.Array() {
		raw := []byte(t.Raw)
		for _, k := range geminiBuiltinToolKeys {
			v := t.Get(k)
			if !v.Exists() {
				continue
			}
			raw, _ = sjson.DeleteBytes(raw, k)
			switch {
			case isGeminiCodeExecutionToolKey(k):
				hasCode = true
			case isGeminiSearchToolKey(k):
				hasSearch = true
				name, val := "googleSearch", v.Raw
				if k == "urlContext" || k == "url_context" {
					name = "urlContext"
				} else if k != "googleSearch" {
					val = "{}"
				}
				if !seen[name] {
					seen[name] = true
					searchTools = append(searchTools, fmt.Sprintf(`{%q:%s}`, name, val))
				}
			}
		}
		if d := gjson.GetBytes(raw, "functionDeclarations"); d.IsArray() && len(d.Array()) > 0 {
			hasFn = true
		}
		if d := gjson.GetBytes(raw, "function_declarations"); d.IsArray() && len(d.Array()) > 0 {
			hasFn = true
		}
		if len(gjson.ParseBytes(raw).Map()) > 0 {
			fnEntries = append(fnEntries, string(raw))
		}
	}
	return
}

// lastTurnIsUserText reports whether the final content is a user turn carrying
// real text (not a functionResponse turn, not the synthetic trailing text).
func lastTurnIsUserText(payload []byte) (bool, int) {
	contents := util.GetGJSONBytesNoCopy(payload, "request.contents")
	arr := contents.Array()
	if len(arr) == 0 {
		return false, -1
	}
	last := arr[len(arr)-1]
	if r := last.Get("role").String(); r != "" && r != "user" {
		return false, -1
	}
	synthetic := antigravityTrailingModelTurnText()
	hasText := false
	for _, p := range last.Get("parts").Array() {
		if p.Get("functionResponse").Exists() {
			return false, -1
		}
		if p.Get("thought").Bool() {
			continue
		}
		if txt := strings.TrimSpace(p.Get("text").String()); txt != "" && txt != synthetic {
			hasText = true
		}
	}
	return hasText, len(arr) - 1
}

func truncateFold(s string) string {
	if len(s) <= builtinPresearchFoldMax {
		return s
	}
	return s[:builtinPresearchFoldMax] + "…[truncated]"
}

// foldGeminiContentsForPresearch rewrites history so it is valid without any
// function declarations: functionCall/functionResponse/code parts become text,
// thoughts and signatures are removed, same-role neighbours are merged.
func foldGeminiContentsForPresearch(contents gjson.Result) []any {
	type turn struct {
		Role  string `json:"role"`
		Parts []any  `json:"parts"`
	}
	var out []*turn
	for _, c := range contents.Array() {
		role := c.Get("role").String()
		if role != "model" {
			role = "user"
		}
		var parts []any
		for _, p := range c.Get("parts").Array() {
			switch {
			case p.Get("thought").Bool():
				continue
			case p.Get("functionCall").Exists():
				fc := p.Get("functionCall")
				parts = append(parts, map[string]any{"text": truncateFold(fmt.Sprintf("[tool call] %s(%s)", fc.Get("name").String(), fc.Get("args").Raw))})
			case p.Get("functionResponse").Exists():
				fr := p.Get("functionResponse")
				parts = append(parts, map[string]any{"text": truncateFold(fmt.Sprintf("[tool result] %s: %s", fr.Get("name").String(), fr.Get("response").Raw))})
			case p.Get("executableCode").Exists():
				parts = append(parts, map[string]any{"text": truncateFold("[code] " + p.Get("executableCode.code").String())})
			case p.Get("codeExecutionResult").Exists():
				parts = append(parts, map[string]any{"text": truncateFold("[code result] " + p.Get("codeExecutionResult.output").String())})
			case p.Get("text").Exists():
				if strings.TrimSpace(p.Get("text").String()) == "" {
					continue
				}
				parts = append(parts, map[string]any{"text": p.Get("text").String()})
			case p.Get("inlineData").Exists() || p.Get("fileData").Exists():
				raw, _ := sjson.Delete(p.Raw, "thoughtSignature")
				var m map[string]any
				if json.Unmarshal([]byte(raw), &m) == nil {
					parts = append(parts, m)
				}
			}
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			continue
		}
		out = append(out, &turn{Role: role, Parts: parts})
	}
	if len(out) > 0 && out[0].Role != "user" {
		out = append([]*turn{{Role: "user", Parts: []any{map[string]any{"text": "."}}}}, out...)
	}
	res := make([]any, len(out))
	for i, t := range out {
		res[i] = t
	}
	return res
}

func buildBuiltinPresearchPayload(payload []byte, searchTools []string) ([]byte, error) {
	out, err := sjson.SetRawBytes(payload, "request.tools", []byte("["+strings.Join(searchTools, ",")+"]"))
	if err != nil {
		return nil, err
	}
	for _, p := range []string{"request.toolConfig", "request.tool_config", "request.generationConfig.responseMimeType", "request.generationConfig.responseSchema", "request.generationConfig.responseJsonSchema", "request.generationConfig.candidateCount"} {
		out, _ = sjson.DeleteBytes(out, p)
	}
	folded := foldGeminiContentsForPresearch(util.GetGJSONBytesNoCopy(payload, "request.contents"))
	if out, err = sjson.SetBytes(out, "request.contents", folded); err != nil {
		return nil, err
	}
	instr := map[string]any{"text": builtinPresearchInstruction}
	if si := gjson.GetBytes(out, "request.systemInstruction.parts"); si.IsArray() {
		out, err = sjson.SetBytes(out, "request.systemInstruction.parts.-1", instr)
	} else {
		out, err = sjson.SetBytes(out, "request.systemInstruction", map[string]any{"role": "user", "parts": []any{instr}})
	}
	return out, err
}

// antigravityBuiltinEmulation carries pass-1 results into the response.
type antigravityBuiltinEmulation struct {
	status     string // searched | no-grounding | failed | function-turn
	grounding  string // raw groundingMetadata
	urlContext string // raw urlContextMetadata
	usage      gjson.Result
	attached   bool
}

func (m *antigravityBuiltinEmulation) headerValue() string {
	if m == nil {
		return ""
	}
	return "emulate; presearch=" + m.status
}

func (m *antigravityBuiltinEmulation) setHeader(h http.Header) http.Header {
	if m == nil {
		return h
	}
	if h == nil {
		h = http.Header{}
	}
	h.Set(antigravityBuiltinToolsHeader, m.headerValue())
	return h
}

func antigravityResponseRoot(body []byte) string {
	if gjson.GetBytes(body, "response").IsObject() {
		return "response."
	}
	return ""
}

var geminiUsageCountFields = []string{"promptTokenCount", "candidatesTokenCount", "thoughtsTokenCount", "toolUsePromptTokenCount", "cachedContentTokenCount", "totalTokenCount"}

func (m *antigravityBuiltinEmulation) addUsage(body []byte, root string, force bool) []byte {
	if m == nil || !m.usage.Exists() {
		return body
	}
	path := root + "usageMetadata"
	if !gjson.GetBytes(body, path).Exists() && !force {
		return body
	}
	for _, f := range geminiUsageCountFields {
		add := m.usage.Get(f).Int()
		if add == 0 {
			continue
		}
		cur := gjson.GetBytes(body, path+"."+f).Int()
		body, _ = sjson.SetBytes(body, path+"."+f, cur+add)
	}
	return body
}

func (m *antigravityBuiltinEmulation) attachGrounding(body []byte, root string) []byte {
	if m == nil || m.attached {
		return body
	}
	cand := root + "candidates.0"
	if !gjson.GetBytes(body, cand).Exists() {
		return body
	}
	if m.grounding != "" && !gjson.GetBytes(body, cand+".groundingMetadata").Exists() {
		body, _ = sjson.SetRawBytes(body, cand+".groundingMetadata", []byte(m.grounding))
	}
	if m.urlContext != "" && !gjson.GetBytes(body, cand+".urlContextMetadata").Exists() {
		body, _ = sjson.SetRawBytes(body, cand+".urlContextMetadata", []byte(m.urlContext))
	}
	m.attached = true
	return body
}

// adjustResponse applies pass-1 usage and grounding to a non-stream body.
func (m *antigravityBuiltinEmulation) adjustResponse(body []byte) []byte {
	if m == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	root := antigravityResponseRoot(body)
	body = m.addUsage(body, root, true)
	return m.attachGrounding(body, root)
}

// adjustSSELine applies pass-1 usage to every usage-bearing frame (usage is
// cumulative per frame) and grounding to the terminal frame.
func (m *antigravityBuiltinEmulation) adjustSSELine(line []byte) []byte {
	if m == nil {
		return line
	}
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := []byte(strings.TrimSpace(trimmed[len("data:"):]))
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	root := antigravityResponseRoot(payload)
	terminal := strings.TrimSpace(gjson.GetBytes(payload, root+"candidates.0.finishReason").String()) != ""
	payload = m.addUsage(payload, root, false)
	if terminal {
		payload = m.attachGrounding(payload, root)
	}
	return append([]byte("data: "), payload...)
}

func formatBuiltinPresearchContext(text string, gm gjson.Result, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Web search results for this message, retrieved live on %s by the Google Search tool]\n", now.UTC().Format("2006-01-02 15:04 UTC"))
	if t := strings.TrimSpace(text); t != "" {
		b.WriteString(t)
		b.WriteString("\n")
	}
	if qs := gm.Get("webSearchQueries").Array(); len(qs) > 0 {
		q := make([]string, 0, len(qs))
		for _, x := range qs {
			q = append(q, x.String())
		}
		fmt.Fprintf(&b, "\nSearch queries: %s\n", strings.Join(q, "; "))
	}
	if chunks := gm.Get("groundingChunks").Array(); len(chunks) > 0 {
		b.WriteString("Sources:\n")
		for i, ch := range chunks {
			w := ch.Get("web")
			fmt.Fprintf(&b, "[%d] %s %s\n", i+1, w.Get("title").String(), w.Get("uri").String())
		}
	}
	b.WriteString("[End of search results. Treat them as current and authoritative over prior knowledge; cite sources where relevant.]")
	return b.String()
}

// injectBuiltinPresearchContext prepends the findings as a text part of the
// last user content (no new turn, so replay indexes are unaffected).
func injectBuiltinPresearchContext(payload []byte, idx int, ctxText string) []byte {
	parts := util.GetGJSONBytesNoCopy(payload, fmt.Sprintf("request.contents.%d.parts", idx))
	if !parts.IsArray() {
		return payload
	}
	part, _ := json.Marshal(map[string]any{"text": ctxText})
	newParts := make([]string, 0, len(parts.Array())+1)
	newParts = append(newParts, string(part))
	for _, p := range parts.Array() {
		newParts = append(newParts, p.Raw)
	}
	out, err := sjson.SetRawBytes(payload, fmt.Sprintf("request.contents.%d.parts", idx), []byte("["+strings.Join(newParts, ",")+"]"))
	if err != nil {
		return payload
	}
	return out
}

// planBuiltinToolsEmulation decides what to do without any I/O. It returns
// pass2 (functions only, nil when emulation does not apply), the pass-1
// payload (nil when no pre-search is due) and the last user content index.
func planBuiltinToolsEmulation(modelName string, payload []byte) (pass2, pass1 []byte, userIdx int) {
	if strings.Contains(strings.ToLower(modelName), "claude") || antigravityBuiltinToolsMode() != builtinToolsModeEmulate {
		return nil, nil, -1
	}
	tools := util.GetGJSONBytesNoCopy(payload, "request.tools")
	if !tools.IsArray() {
		return nil, nil, -1
	}
	fnEntries, searchTools, hasFn, hasSearch, hasCode := splitGeminiTools(tools)
	if !hasFn || !hasSearch {
		return nil, nil, -1
	}
	if hasCode {
		log.Debug("antigravity: builtin-tools emulation skipped (codeExecution with functions is passed through)")
		return nil, nil, -1
	}
	out, err := sjson.SetRawBytes(payload, "request.tools", []byte("["+strings.Join(fnEntries, ",")+"]"))
	if err != nil {
		return nil, nil, -1
	}
	ok, idx := lastTurnIsUserText(out)
	if !ok {
		return out, nil, -1
	}
	p1, err := buildBuiltinPresearchPayload(payload, searchTools)
	if err != nil {
		return out, nil, -1
	}
	return out, p1, idx
}

// prepareBuiltinToolsEmulation returns the payload to send, the payload for
// reasoning-replay bookkeeping (without injected search text) and the pass-1
// state (nil when emulation does not apply). It never fails the request.
func (e *AntigravityExecutor) prepareBuiltinToolsEmulation(ctx context.Context, auth *cliproxyauth.Auth, token, modelName, baseURL string, httpClient *http.Client, payload []byte, attemptLimit time.Duration, sessionID string) (send, replay []byte, emu *antigravityBuiltinEmulation) {
	pass2, pass1, idx := planBuiltinToolsEmulation(modelName, payload)
	if pass2 == nil {
		return payload, payload, nil
	}
	if pass1 == nil {
		return pass2, pass2, &antigravityBuiltinEmulation{status: "function-turn"}
	}
	emu = &antigravityBuiltinEmulation{status: "failed"}
	started := time.Now()
	fields := log.Fields{"model": modelName, "auth": auth.ID}
	timeout := antigravityBuiltinPresearchTimeout(attemptLimit)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := e.buildRequest(pctx, auth, token, modelName, pass1, false, "", baseURL, sessionID)
	if err != nil {
		log.WithFields(fields).Warnf("antigravity: builtin-tools pre-search build failed, continuing without search: %v", err)
		return pass2, pass2, emu
	}
	resp, err := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "builtin_presearch").Do(httpReq)
	if err != nil {
		log.WithFields(fields).Warnf("antigravity: builtin-tools pre-search failed after %s (timeout %s), continuing without search: %v", time.Since(started).Round(time.Millisecond), timeout, err)
		return pass2, pass2, emu
	}
	body, errRead := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if errRead != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.WithFields(fields).Warnf("antigravity: builtin-tools pre-search status %d (%v), continuing without search: %s", resp.StatusCode, errRead, helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), body))
		return pass2, pass2, emu
	}
	root := antigravityResponseRoot(body)
	emu.usage = gjson.GetBytes(body, root+"usageMetadata")
	cand := gjson.GetBytes(body, root+"candidates.0")
	var text strings.Builder
	for _, p := range cand.Get("content.parts").Array() {
		if !p.Get("thought").Bool() {
			text.WriteString(p.Get("text").String())
		}
	}
	gm := cand.Get("groundingMetadata")
	uc := cand.Get("urlContextMetadata")
	if len(gm.Get("groundingChunks").Array()) == 0 && len(gm.Get("webSearchQueries").Array()) == 0 && !uc.Exists() {
		emu.status = "no-grounding"
		log.WithFields(fields).Debugf("antigravity: builtin-tools pre-search returned no grounding in %s", time.Since(started).Round(time.Millisecond))
		return pass2, pass2, emu
	}
	if gm.Exists() {
		emu.grounding = gm.Raw
	}
	if uc.Exists() {
		emu.urlContext = uc.Raw
	}
	emu.status = "searched"
	log.WithFields(fields).Debugf("antigravity: builtin-tools pre-search ok in %s (%d sources)", time.Since(started).Round(time.Millisecond), len(gm.Get("groundingChunks").Array()))
	return injectBuiltinPresearchContext(pass2, idx, formatBuiltinPresearchContext(text.String(), gm, time.Now())), pass2, emu
}
