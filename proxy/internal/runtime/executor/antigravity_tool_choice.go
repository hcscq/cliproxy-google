package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Antigravity's v1internal endpoint ignores request.toolConfig for Gemini
// models (verified 2026-10: mode ANY still answers with text, and
// allowedFunctionNames does not stop other declared functions from being
// called). When a client restricts the callable functions (OpenAI named
// tool_choice, Claude {"type":"tool"}, Gemini allowedFunctionNames), only the
// allowed declarations are sent upstream so a forbidden function can never be
// called. Forcing a call (ANY/required) cannot be emulated and stays
// best-effort.
func restrictGeminiToolsToAllowed(payload []byte) []byte {
	allowed := gjson.GetBytes(payload, "request.toolConfig.functionCallingConfig.allowedFunctionNames")
	if !allowed.IsArray() || len(allowed.Array()) == 0 {
		return payload
	}
	if m := strings.ToUpper(gjson.GetBytes(payload, "request.toolConfig.functionCallingConfig.mode").String()); m != "" && m != "ANY" && m != "VALIDATED" {
		return payload
	}
	names := map[string]bool{}
	for _, n := range allowed.Array() {
		names[n.String()] = true
	}
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() {
		return payload
	}
	kept := 0
	out := payload
	for i, tool := range tools.Array() {
		decls := tool.Get("functionDeclarations")
		if !decls.IsArray() {
			continue
		}
		var filtered []any
		for _, d := range decls.Array() {
			if names[d.Get("name").String()] {
				filtered = append(filtered, d.Value())
			}
		}
		kept += len(filtered)
		out, _ = sjson.SetBytes(out, "request.tools."+itoa(i)+".functionDeclarations", filtered)
	}
	if kept == 0 { // names did not match any declaration: leave the request untouched
		return payload
	}
	// drop tool entries left without declarations or other tool kinds
	res := gjson.GetBytes(out, "request.tools").Array()
	var cleaned []any
	for _, t := range res {
		if d := t.Get("functionDeclarations"); d.Exists() && (!d.IsArray() || len(d.Array()) == 0) && len(t.Map()) == 1 {
			continue
		}
		cleaned = append(cleaned, t.Value())
	}
	out, _ = sjson.SetBytes(out, "request.tools", cleaned)
	return out
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
