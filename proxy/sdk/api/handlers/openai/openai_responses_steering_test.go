package openai

import (
	"strings"
	"testing"
)

// TestResponsesSteerRejectedWhenDisabled verifies that when codex.response-steering is
// false (default), response.steer is rejected as an unsupported request type and is
// never rewritten or converted into response.create.
func TestResponsesSteerRejectedWhenDisabled(t *testing.T) {
	raw := []byte(`{"type":"response.steer","previous_response_id":"resp-1","input":"Keep the scope small."}`)
	lastRequest := []byte(`{"model":"gpt-6-astra","stream":true,"input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, []byte("[]"))
	if errMsg == nil {
		t.Fatal("expected response.steer to be rejected when steering is disabled")
	}
	if !strings.Contains(errMsg.Error.Error(), "unsupported websocket request type: response.steer") {
		t.Fatalf("unexpected error message: %v", errMsg.Error)
	}
}
