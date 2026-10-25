package logging

import (
	"strings"
	"testing"
)

func TestErrorSampleType(t *testing.T) {
	cases := map[string]string{
		`{"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 131072."}}`: "400:input-token-exceeds",
		`{"error":{"message":"No capacity available for model gemini-2.5-pro on the server"}}`:                         "400:no-capacity",
		`unknown provider for model foo-123`: "400:model-not-found",
		`{"error":{"code":"model_cooldown","message":"All credentials for model gemini-3-pro are cooling down"}}`: "400:model-cooldown",
	}
	for in, want := range cases {
		if got := ErrorSampleType(400, in); got != want {
			t.Fatalf("%s: got %s want %s", in, got, want)
		}
	}
	a := ErrorSampleType(500, `weird failure id 8f2a9c1d2e3f4a5b at 12345`)
	b := ErrorSampleType(500, `weird failure id 0000aaaa1111bbbb at 999`)
	if a != b || len(a) > 60 {
		t.Fatalf("not normalized: %s vs %s", a, b)
	}
}

func TestRedactErrorSampleText(t *testing.T) {
	in := "Authorization: Bearer ya29.abcdefghijkl\n{\"access_token\":\"secret1\"}\nhttps://x/y?key=AIzaSECRET&alt=sse"
	out := RedactErrorSampleText(in)
	for _, s := range []string{"ya29.abcdef", "secret1", "AIzaSECRET"} {
		if strings.Contains(out, s) {
			t.Fatalf("leaked %s: %s", s, out)
		}
	}
	h, p := redactErrorSampleHeaders(map[string][]string{"Authorization": {"Bearer sk-abcdef123"}, "X-Foo": {"1"}})
	if h["Authorization"][0] != "[REDACTED]" || p != "sk-abc" {
		t.Fatalf("bad %v %s", h, p)
	}
}
