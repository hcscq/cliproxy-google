package executor

import (
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
