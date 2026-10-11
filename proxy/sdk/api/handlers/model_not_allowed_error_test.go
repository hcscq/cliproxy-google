package handlers

import (
	"net/http"
	"strings"
	"testing"
)

func TestModelNotAllowedOpenAIErrorCode(t *testing.T) {
	body := string(BuildErrorResponseBodyWithError(http.StatusForbidden, "model_not_allowed: model x is not allowed for this API key", nil))
	if !strings.Contains(body, `"code":"model_not_allowed"`) || !strings.Contains(body, `"type":"permission_error"`) {
		t.Fatalf("body %s", body)
	}
	other := string(BuildErrorResponseBodyWithError(http.StatusForbidden, "forbidden", nil))
	if strings.Contains(other, "model_not_allowed") {
		t.Fatalf("unrelated 403 must keep its code: %s", other)
	}
}
