package auth

import (
	"net/http"
	"testing"
)

func TestContentBlockUsageResultStaysSuccessful(t *testing.T) {
	body := `{"type":"content_block","block_reason":"PROHIBITED_CONTENT","block_reason_message":"The prompt is blocked due to safety"}`
	result := NewUsageResult("auth-1", "antigravity", "gemini-test", http.StatusOK, body)
	if !result.Success || result.Error != nil {
		t.Fatalf("content block usage must not penalize the credential: %+v", result)
	}
}
