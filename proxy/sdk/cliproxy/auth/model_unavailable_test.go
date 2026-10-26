package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestModelUnavailableDefaultAndMatching(t *testing.T) {
	SetUnavailableModels(nil)
	defer SetUnavailableModels(nil)
	for _, m := range []string{"gemini-2.5-pro", "GEMINI-2.5-PRO", "models/gemini-2.5-pro", "gemini-2.5-pro(8192)"} {
		if !modelUnavailable(m) {
			t.Fatalf("%q should be unavailable by default", m)
		}
	}
	for _, m := range []string{"gemini-2.5-flash", "gemini-2.5-pro-preview-tts", "gemini-3.8-flash-tiered", "gemini-4-argon-high", ""} {
		if modelUnavailable(m) {
			t.Fatalf("%q should be available", m)
		}
	}
	off := "off"
	SetUnavailableModels(&off)
	if modelUnavailable("gemini-2.5-pro") {
		t.Fatal("off should disable")
	}
	empty := ""
	SetUnavailableModels(&empty)
	if modelUnavailable("gemini-2.5-pro") {
		t.Fatal("empty should disable")
	}
	list := "foo-1; gemini-9*\nbar"
	SetUnavailableModels(&list)
	if !modelUnavailable("gemini-9-x") || !modelUnavailable("bar") || modelUnavailable("gemini-2.5-pro") {
		t.Fatal("custom list mismatch")
	}
}

func TestModelUnavailableShortCircuitsExecute(t *testing.T) {
	SetUnavailableModels(nil)
	defer SetUnavailableModels(nil)
	m := &Manager{} // no executors/auths: any selection attempt would fail differently
	check := func(err error) {
		t.Helper()
		if err == nil || !IsModelUnavailableError(err) {
			t.Fatalf("want model_unavailable, got %v", err)
		}
		var se interface{ StatusCode() int }
		if !errors.As(err, &se) || se.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %v", err)
		}
		h := err.(interface{ Headers() http.Header }).Headers()
		if SafeResponseHeaders(err).Get("Retry-After") != "600" {
			t.Fatalf("SafeResponseHeaders Retry-After = %q", SafeResponseHeaders(err).Get("Retry-After"))
		}
		if h.Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
		var ae *Error
		if !errors.As(err, &ae) || ae.Code != "model_unavailable" {
			t.Fatalf("want *Error code model_unavailable, got %#v", ae)
		}
		if want := "model gemini-2.5-pro currently unavailable upstream (no capacity)"; ae.Message != want {
			t.Fatalf("message %q", ae.Message)
		}
	}
	req := cliproxyexecutor.Request{Model: "gemini-2.5-pro"}
	_, err := m.Execute(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
	check(err)
	_, err = m.ExecuteStream(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
	check(err)
	_, err = m.ExecuteCount(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
	check(err)
}
