package executor

import (
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityDefaultTrailingModelTurnText = "Continue."

// antigravityTrailingModelTurnText returns the text for the user turn appended
// after a trailing model turn, or "" when appending is disabled.
func antigravityTrailingModelTurnText() string {
	if live := antigravityHealthLive.Load(); live != nil && live.TrailingModelTurnText != nil {
		v := strings.TrimSpace(*live.TrailingModelTurnText)
		if strings.EqualFold(v, "off") || v == "" {
			return ""
		}
		return v
	}
	return antigravityDefaultTrailingModelTurnText
}

func antigravityDropEmptyPartsEnabled() bool {
	if live := antigravityHealthLive.Load(); live != nil && live.DropEmptyParts != nil {
		return *live.DropEmptyParts
	}
	return true
}

// geminiPartDataKeys are the members of the Part.data oneof.
var geminiPartDataKeys = []string{"text", "inlineData", "inline_data", "fileData", "file_data", "functionCall", "function_call",
	"functionResponse", "function_response", "executableCode", "executable_code", "codeExecutionResult", "code_execution_result"}

func geminiPartHasData(part gjson.Result) bool {
	if !part.IsObject() {
		return false
	}
	for _, k := range geminiPartDataKeys {
		if part.Get(k).Exists() {
			return true
		}
	}
	return false
}

// dropGeminiEmptyParts removes parts with no data field and contents that end
// up with no parts. Returns the payload unchanged when nothing is removed.
func dropGeminiEmptyParts(payload []byte, path string) []byte {
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	changed := false
	items := make([][]byte, 0, len(contents.Array()))
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			items = append(items, []byte(content.Raw))
			continue
		}
		kept := make([][]byte, 0, len(parts.Array()))
		for _, part := range parts.Array() {
			if geminiPartHasData(part) {
				kept = append(kept, []byte(part.Raw))
			}
		}
		if len(kept) == len(parts.Array()) {
			items = append(items, []byte(content.Raw))
			continue
		}
		changed = true
		if len(kept) == 0 {
			continue
		}
		updated, err := sjson.SetRawBytes([]byte(content.Raw), "parts", translatorcommon.JoinRawArray(kept))
		if err != nil {
			return payload
		}
		items = append(items, updated)
	}
	if !changed {
		return payload
	}
	out, err := sjson.SetRawBytes(payload, path, translatorcommon.JoinRawArray(items))
	if err != nil {
		return payload
	}
	return out
}

// fixGeminiTrailingModelTurn makes a request that ends with a model turn (or
// with the synthetic empty user turn appended after one) end with a non-empty
// user turn. Function-response turns are left alone: upstream expects the model
// to answer them.
func fixGeminiTrailingModelTurn(payload []byte, path, text string) []byte {
	if text == "" {
		return payload
	}
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	arr := contents.Array()
	n := len(arr)
	if n == 0 {
		return payload
	}
	last := arr[n-1]
	turn, _ := sjson.SetBytes([]byte(`{"role":"user","parts":[{"text":""}]}`), "parts.0.text", text)
	isModel := func(c gjson.Result) bool {
		r := c.Get("role").String()
		return r == "model" || r == "assistant"
	}
	switch {
	case isModel(last) && !contentHasFunctionResponseParts(last):
		out, err := sjson.SetRawBytes(payload, path+".-1", turn)
		if err != nil {
			return payload
		}
		return out
	case n >= 2 && last.Get("role").String() == "user" && geminiContentIsBlankText(last) && isModel(arr[n-2]) && !contentHasFunctionResponseParts(arr[n-2]):
		out, err := sjson.SetBytes(payload, path+"."+strconv.Itoa(n-1)+".parts", []map[string]string{{"text": text}})
		if err != nil {
			return payload
		}
		return out
	}
	return payload
}

func contentHasFunctionResponseParts(content gjson.Result) bool {
	found := false
	content.Get("parts").ForEach(func(_, p gjson.Result) bool {
		found = p.Get("functionResponse").Exists() || p.Get("function_response").Exists()
		return !found
	})
	return found
}

func geminiContentIsBlankText(content gjson.Result) bool {
	parts := content.Get("parts").Array()
	if len(parts) == 0 {
		return true
	}
	for _, p := range parts {
		if !p.Get("text").Exists() || strings.TrimSpace(p.Get("text").String()) != "" {
			return false
		}
		for _, k := range geminiPartDataKeys[1:] {
			if p.Get(k).Exists() {
				return false
			}
		}
	}
	return true
}

func antigravityRepairFunctionResponseNamesEnabled() bool {
	if live := antigravityHealthLive.Load(); live != nil && live.RepairFunctionResponseNames != nil {
		return *live.RepairFunctionResponseNames
	}
	return true
}

func antigravityDropBuiltinToolsWithFunctions() bool {
	return antigravityBuiltinToolsMode() == builtinToolsModeDrop
}

type geminiPendingCall struct{ id, name string }

// repairGeminiFunctionResponseNames fills functionResponse.name from the
// functionCall it answers when the client sent an empty name or one that
// matches none of the pending calls (OpenAI-style tool results carry only the
// call id; some clients then emit "unknown_function"). The call is found by id,
// else by position when the counts match. Valid names are never touched.
func repairGeminiFunctionResponseNames(payload []byte, path string) []byte {
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	out := payload
	var pending []geminiPendingCall
	for ci, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		var calls []geminiPendingCall
		type respRef struct {
			idx     int
			key, id string
			name    string
		}
		var resps []respRef
		for pi, part := range parts.Array() {
			for _, k := range []string{"functionCall", "function_call"} {
				if c := part.Get(k); c.Exists() {
					calls = append(calls, geminiPendingCall{id: c.Get("id").String(), name: c.Get("name").String()})
				}
			}
			for _, k := range []string{"functionResponse", "function_response"} {
				if r := part.Get(k); r.Exists() {
					resps = append(resps, respRef{idx: pi, key: k, id: r.Get("id").String(), name: r.Get("name").String()})
				}
			}
		}
		if len(calls) > 0 {
			pending = calls
			continue
		}
		if len(resps) == 0 || len(pending) == 0 {
			continue
		}
		names := map[string]bool{}
		for _, c := range pending {
			names[c.name] = true
		}
		for ri, r := range resps {
			if r.name != "" && names[r.name] {
				continue
			}
			target := ""
			if r.id != "" {
				for _, c := range pending {
					if c.id == r.id {
						target = c.name
						break
					}
				}
			}
			if target == "" && len(resps) == len(pending) {
				target = pending[ri].name
			}
			if target == "" {
				continue
			}
			p := path + "." + strconv.Itoa(ci) + ".parts." + strconv.Itoa(r.idx) + "." + r.key + ".name"
			if updated, err := sjson.SetBytes(out, p, target); err == nil {
				out = updated
			}
		}
		pending = nil
	}
	return out
}

var geminiBuiltinToolKeys = []string{"googleSearch", "google_search", "googleSearchRetrieval", "google_search_retrieval", "codeExecution", "code_execution", "urlContext", "url_context"}

// dropGeminiBuiltinToolsWhenFunctions removes built-in tool entries when the
// request also declares functions; Antigravity rejects the mix (400 "Please
// enable tool_config.include_server_side_tool_invocations") and ignores
// toolConfig, so the function tools are kept. Requests with only built-in
// tools, or only functions, are unchanged.
func dropGeminiBuiltinToolsWhenFunctions(payload []byte, path string) []byte {
	tools := util.GetGJSONBytesNoCopy(payload, path)
	if !tools.IsArray() {
		return payload
	}
	hasFn, hasBuiltin := false, false
	for _, t := range tools.Array() {
		if d := t.Get("functionDeclarations"); d.IsArray() && len(d.Array()) > 0 {
			hasFn = true
		}
		if d := t.Get("function_declarations"); d.IsArray() && len(d.Array()) > 0 {
			hasFn = true
		}
		for _, k := range geminiBuiltinToolKeys {
			if t.Get(k).Exists() {
				hasBuiltin = true
			}
		}
	}
	if !hasFn || !hasBuiltin {
		return payload
	}
	kept := make([]string, 0, len(tools.Array()))
	dropped := 0
	for _, t := range tools.Array() {
		raw := []byte(t.Raw)
		for _, k := range geminiBuiltinToolKeys {
			if t.Get(k).Exists() {
				raw, _ = sjson.DeleteBytes(raw, k)
				dropped++
			}
		}
		if len(gjson.ParseBytes(raw).Map()) == 0 {
			continue
		}
		kept = append(kept, string(raw))
	}
	updated, err := sjson.SetRawBytes(payload, path, []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return payload
	}
	log.Warnf("antigravity: dropped %d built-in tool(s) sent together with function declarations (upstream rejects the mix; setting builtin-tools-with-functions=keep to pass through)", dropped)
	return updated
}
