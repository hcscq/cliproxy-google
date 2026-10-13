package executor

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// foldAntigravityCountTokensPayload moves systemInstruction and tool
// declarations into leading user contents. The Antigravity countTokens
// endpoint only counts request.contents and silently ignores both fields, so
// a Claude Code request with a ~12k-token system prompt and tools was reported
// as a handful of tokens. Tools are counted via their JSON declaration text,
// which approximates how the model sees them.
func foldAntigravityCountTokensPayload(payload []byte) []byte {
	var lead [][]byte
	if parts := gjson.GetBytes(payload, "request.systemInstruction.parts"); parts.IsArray() {
		for _, p := range parts.Array() {
			if t := p.Get("text").String(); t != "" {
				part, _ := sjson.SetBytes([]byte(`{}`), "text", t)
				lead = append(lead, part)
			}
		}
	}
	if tools := gjson.GetBytes(payload, "request.tools"); tools.IsArray() && len(tools.Array()) > 0 {
		part, _ := sjson.SetBytes([]byte(`{}`), "text", tools.Raw)
		lead = append(lead, part)
	}
	payload, _ = sjson.DeleteBytes(payload, "request.systemInstruction")
	payload, _ = sjson.DeleteBytes(payload, "request.tools")
	if len(lead) == 0 {
		return payload
	}
	// Append to the leading user turn when there is one so the content order
	// (and any synthetic leading user part) stays intact; otherwise prepend.
	if gjson.GetBytes(payload, "request.contents.0.role").String() == "user" {
		for _, p := range lead {
			payload, _ = sjson.SetRawBytes(payload, "request.contents.0.parts.-1", p)
		}
		return payload
	}
	content := []byte(`{"role":"user","parts":[]}`)
	for _, p := range lead {
		content, _ = sjson.SetRawBytes(content, "parts.-1", p)
	}
	out := []byte(`[]`)
	out, _ = sjson.SetRawBytes(out, "-1", content)
	for _, c := range gjson.GetBytes(payload, "request.contents").Array() {
		out, _ = sjson.SetRawBytes(out, "-1", []byte(c.Raw))
	}
	payload, _ = sjson.SetRawBytes(payload, "request.contents", out)
	return payload
}
