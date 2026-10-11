package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestDispatchRejectsModelOutsideKeyGroups(t *testing.T) {
	m := NewManager(nil, nil, nil)
	_, err := m.Dispatch(context.Background(), []string{"antigravity"}, "gemini-3-flash", Options{Metadata: map[string]any{AllowedModelIDsMetadataKey: []string{"gemini-3.8-flash-tiered"}}})
	var e *Error
	if !errors.As(err, &e) || e.Code != "model_not_allowed" || e.HTTPStatus != http.StatusForbidden {
		t.Fatalf("want 403 model_not_allowed, got %#v", err)
	}
}
