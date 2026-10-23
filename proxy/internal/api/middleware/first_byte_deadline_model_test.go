package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestModel(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1beta/models/gemini-4-argon-high:streamGenerateContent?alt=sse", nil)
	if m := requestModel(r); m != "gemini-4-argon-high" {
		t.Fatalf("path model %q", m)
	}
	r = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gemini-4-argon","stream":true}`))
	if m := requestModel(r); m != "gemini-4-argon" {
		t.Fatalf("body model %q", m)
	}
	if !requestIsStreaming(r) {
		t.Fatal("body must be restored")
	}
}
