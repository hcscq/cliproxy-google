package gemini

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeGeminiRequestsField handles a top-level "requests" key on
// generateContent / streamGenerateContent bodies (not a GenerateContentRequest
// field; Google rejects it with 400 Unknown name "requests"):
//   - normal body ("contents") + a stray "requests" with no request objects in it:
//     the stray key is dropped (it carries nothing generateContent could use);
//   - "contents" + a real batch of request objects: ambiguous, clear 400;
//   - batch body without "contents" and more than one request: clear 400
//     (generateContent returns one response; Antigravity has no batch endpoint).
//
// A single-request batch body is left for the translator to unwrap.
// note describes what was dropped (key names / sizes only, never content).
func normalizeGeminiRequestsField(raw []byte) (out []byte, errMsg, note string) {
	reqs := gjson.GetBytes(raw, "requests")
	if !reqs.Exists() {
		return raw, "", ""
	}
	isRequest := func(v gjson.Result) bool {
		if inner := v.Get("request"); inner.IsObject() {
			v = inner
		}
		return v.IsObject() && v.Get("contents").Exists()
	}
	batch := 0
	if reqs.IsArray() {
		for _, it := range reqs.Array() {
			if isRequest(it) {
				batch++
			}
		}
	}
	if gjson.GetBytes(raw, "contents").Exists() {
		if batch > 0 {
			return nil, fmt.Sprintf(`request has both "contents" and a batch "requests" array (%d requests); generateContent takes one request, send the batch items as separate calls`, batch), ""
		}
		kind, n := reqs.Type.String(), 0
		if reqs.IsArray() {
			kind, n = "array", len(reqs.Array())
		} else if reqs.IsObject() {
			kind = "object"
			reqs.ForEach(func(_, _ gjson.Result) bool { n++; return true })
		}
		cleaned, err := sjson.DeleteBytes(raw, "requests")
		if err != nil {
			return raw, "", ""
		}
		return cleaned, "", fmt.Sprintf("dropped stray top-level \"requests\" (%s, %d items) from generateContent body", kind, n)
	}
	if batch > 1 {
		return nil, fmt.Sprintf(`batch body with %d "requests" is not supported on generateContent/streamGenerateContent (one response per call; this channel has no batch endpoint); send one request per call`, batch), ""
	}
	return raw, "", ""
}
