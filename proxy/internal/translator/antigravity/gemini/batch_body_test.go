package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToAntigravityUnwrapsSingleBatchRequest(t *testing.T) {
	for _, body := range []string{
		`{"requests":[{"model":"models/gemini-3-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]}]}`,
		`{"requests":[{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}]}`,
	} {
		out, err := ConvertGeminiRequestToAntigravity("gemini-3-flash", []byte(body), false)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(out, "request.requests").Exists() || gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != "hi" || gjson.GetBytes(out, "request.model").Exists() {
			t.Fatalf("not unwrapped: %s", out)
		}
	}
	multi := []byte(`{"requests":[{"contents":[]},{"contents":[]}]}`)
	if got := unwrapGeminiBatchBody(multi); string(got) != string(multi) {
		t.Fatalf("multi must stay unchanged: %s", got)
	}
}
