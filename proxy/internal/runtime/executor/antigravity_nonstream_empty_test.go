package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityNonStreamEmpty(t *testing.T) {
	cases := map[string]bool{
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[]}}]}}`:                                     true,
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":""}]}}]}}`:                          true,
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hm","thought":true}]}}]}}`:         true,
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"thoughtSignature":"abc","text":""}]}}]}}`: true,
		`{"response":{"candidates":[]}}`:                           true,
		`{"response":{}}`:                                          true,
		`{"response":{"promptFeedback":{"blockReason":"SAFETY"}}}`: false,
		`{"response":{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[]}}]}}`:                        false,
		`{"response":{"candidates":[{"finishReason":"SAFETY"}]}}`:                                                   false,
		`{"response":{"candidates":[{"finishReason":"PROHIBITED_CONTENT"}]}}`:                                       false,
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hi"}]}}]}}`:                 false,
		`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"f"}}]}}]}}`: false,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"plain"}]}}]}`:                           false,
	}
	cases["data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n"] = false
	for body, want := range cases {
		if got := antigravityNonStreamEmpty([]byte(body)); got != want {
			t.Errorf("%s: got %v want %v", body, got, want)
		}
	}
}

func TestAntigravityRetryEmptyToggle(t *testing.T) {
	defer antigravityHealthLive.Store(antigravityHealthLive.Load())
	off := false
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RetryEmptyResponse: &off})
	if antigravityRetryEmptyStreamEnabled() {
		t.Fatal("panel off must disable")
	}
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{})
	if !antigravityRetryEmptyStreamEnabled() {
		t.Fatal("default on")
	}
}
