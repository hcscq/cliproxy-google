package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestExecuteStreamRetriesAfterInvalidThinkingSignature400(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	on := config.CompatModeOn
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RetryStripInvalidThinkingOn400: &on})

	var hits atomic.Int32
	var firstHadSig, secondHadSig atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := hits.Add(1)
		hasSig := strings.Contains(string(body), `"thoughtSignature":"foreign-sig-for-retry"`)
		switch n {
		case 1:
			firstHadSig.Store(hasSig)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"messages.1.content.0: Invalid signature in thinking block","status":"INVALID_ARGUMENT"}}`))
		case 2:
			secondHadSig.Store(hasSig)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n")
		default:
			t.Errorf("unexpected request #%d", n)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ex := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID: "auth-stream-thinking-retry",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	// Pre-translated Antigravity body: Claude models skip Gemini signature
	// sanitization, so the foreign thoughtSignature reaches strip-on-retry.
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"thought":true,"text":"secret","thoughtSignature":"foreign-sig-for-retry"},{"text":"prev"}]},{"role":"user","parts":[{"text":"continue"}]}]}}`)
	result, err := ex.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatAntigravity,
		ResponseFormat:  sdktranslator.FormatAntigravity,
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error (hits=%d firstHadSig=%v): %v", hits.Load(), firstHadSig.Load(), err)
	}
	var out strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		out.Write(chunk.Payload)
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want 2; firstHadSig=%v secondHadSig=%v", hits.Load(), firstHadSig.Load(), secondHadSig.Load())
	}
	if !firstHadSig.Load() {
		t.Fatal("first upstream request should carry foreign-sig-for-retry")
	}
	if secondHadSig.Load() {
		t.Fatal("retry upstream request should not carry foreign-sig-for-retry")
	}
	if !strings.Contains(out.String(), "recovered") {
		t.Fatalf("client stream missing recovered text: %s", out.String())
	}
}

func TestStripInvalidThinkingDecisionRemovedZero(t *testing.T) {
	prev := antigravityHealthLive.Load()
	defer antigravityHealthLive.Store(prev)
	on := config.CompatModeOn
	antigravityHealthLive.Store(&config.AntigravityHealthConfig{RetryStripInvalidThinkingOn400: &on})

	_, removed, retry := antigravityStripInvalidThinkingForRetryDecision([]byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`))
	if removed != 0 || retry {
		t.Fatalf("want removed=0 retry=false, got removed=%d retry=%v", removed, retry)
	}
}
