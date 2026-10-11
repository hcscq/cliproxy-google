package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestRetiredModelErrorRequiresAllCredentialsRetired(t *testing.T) {
	retired := func() *ModelState {
		return &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), UpdatedAt: time.Now(),
			LastError: &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"code":404,"message":"model x has been retired upstream (model_not_supported): Gemini 3.5 Flash is no longer available.","details":[{"reason":"MODEL_RETIRED"}]}}`}}
	}
	m := &Manager{auths: map[string]*Auth{
		"a": {ID: "a", Provider: "antigravity", ModelStates: map[string]*ModelState{"gemini-3.5-flash-low": retired()}},
		"b": {ID: "b", Provider: "antigravity", ModelStates: map[string]*ModelState{"gemini-3.5-flash-low": retired()}},
		"c": {ID: "c", Provider: "antigravity"}, // never used the model: ignored
	}}
	err := m.retiredModelError([]string{"antigravity"}, "gemini-3.5-flash-low")
	e, ok := err.(*Error)
	if !ok || e.Message != "model x has been retired upstream (model_not_supported): Gemini 3.5 Flash is no longer available." || e.HTTPStatus != http.StatusNotFound || e.Code != "model_not_found" || e.Retryable {
		t.Fatalf("want 404 model_not_found, got %#v", err)
	}
	m.auths["b"].ModelStates["gemini-3.5-flash-low"] = &ModelState{Status: StatusActive}
	if err := m.retiredModelError([]string{"antigravity"}, "gemini-3.5-flash-low"); err != nil {
		t.Fatalf("a credential that still serves the model must disable the shortcut: %v", err)
	}
	if err := m.retiredModelError([]string{"antigravity"}, "gemini-3-flash"); err != nil {
		t.Fatalf("unrelated model: %v", err)
	}
}
