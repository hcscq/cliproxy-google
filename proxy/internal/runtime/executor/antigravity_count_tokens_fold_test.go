package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestFoldAntigravityCountTokensPayload(t *testing.T) {
	in := []byte(`{"request":{"systemInstruction":{"role":"user","parts":[{"text":"SYS"}]},"tools":[{"functionDeclarations":[{"name":"Read"}]}],"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	out := foldAntigravityCountTokensPayload(in)
	if gjson.GetBytes(out, "request.systemInstruction").Exists() || gjson.GetBytes(out, "request.tools").Exists() {
		t.Fatalf("system/tools not removed: %s", out)
	}
	c := gjson.GetBytes(out, "request.contents").Array()
	if len(c) != 1 || c[0].Get("parts.0.text").String() != "hi" || c[0].Get("parts.1.text").String() != "SYS" || !gjson.Parse(c[0].Get("parts.2.text").String()).IsArray() {
		t.Fatalf("unexpected contents: %s", out)
	}
	modelFirst := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"SYS"}]},"contents":[{"role":"model","parts":[{"text":"x"}]}]}}`)
	if mf := gjson.GetBytes(foldAntigravityCountTokensPayload(modelFirst), "request.contents"); mf.Get("#").Int() != 2 || mf.Get("0.parts.0.text").String() != "SYS" {
		t.Fatalf("model-first not prepended: %s", mf.Raw)
	}
	plain := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	if got := gjson.GetBytes(foldAntigravityCountTokensPayload(plain), "request.contents.#").Int(); got != 1 {
		t.Fatalf("plain payload changed: %d", got)
	}
}
