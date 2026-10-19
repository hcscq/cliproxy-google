package gemini

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const oneContent = `"contents":[{"role":"user","parts":[{"text":"hi"}]}]`

func TestNormalizeGeminiRequestsField(t *testing.T) {
	// Observed prod shape: valid request + stray "requests" -> stripped, rest intact.
	for _, stray := range []string{`[]`, `null`, `{"a":1}`, `["x"]`, `5`} {
		in := []byte(`{` + oneContent + `,"requests":` + stray + `,"generationConfig":{"temperature":0.2}}`)
		out, errMsg, note := normalizeGeminiRequestsField(in)
		if errMsg != "" || gjson.GetBytes(out, "requests").Exists() || !gjson.GetBytes(out, "contents").Exists() || gjson.GetBytes(out, "generationConfig.temperature").Float() != 0.2 || note == "" {
			t.Fatalf("stray %s: out=%s err=%q note=%q", stray, out, errMsg, note)
		}
	}
	// contents + real batch -> clear 400.
	if _, errMsg, _ := normalizeGeminiRequestsField([]byte(`{` + oneContent + `,"requests":[{` + oneContent + `}]}`)); !strings.Contains(errMsg, `both "contents" and a batch`) {
		t.Fatalf("ambiguous: %q", errMsg)
	}
	// multi-request batch -> clear 400.
	if _, errMsg, _ := normalizeGeminiRequestsField([]byte(`{"requests":[{` + oneContent + `},{"request":{` + oneContent + `}}]}`)); !strings.Contains(errMsg, "2 \"requests\" is not supported") {
		t.Fatalf("multi: %q", errMsg)
	}
	// single-request batch and plain bodies untouched (translator unwraps the former).
	for _, body := range []string{`{"requests":[{` + oneContent + `}]}`, `{` + oneContent + `}`} {
		out, errMsg, note := normalizeGeminiRequestsField([]byte(body))
		if string(out) != body || errMsg != "" || note != "" {
			t.Fatalf("untouched %s: %s %q %q", body, out, errMsg, note)
		}
	}
}
