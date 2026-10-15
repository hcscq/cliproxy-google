package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func runAntigravityRawStream(t *testing.T, sse string) ([][]byte, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer server.Close()
	ex := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	result, err := ex.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "t", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "p"},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true})
	if err != nil {
		return nil, err
	}
	var chunks [][]byte
	for c := range result.Chunks {
		if c.Err != nil {
			if len(chunks) > 0 {
				t.Fatalf("error after %d chunks were already sent", len(chunks))
			}
			return nil, c.Err
		}
		chunks = append(chunks, c.Payload)
	}
	return chunks, nil
}

func TestAntigravityEmptyStreamFailsRetryablyBeforeOutput(t *testing.T) {
	_, err := runAntigravityRawStream(t, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"totalTokenCount":2}}}

`)
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("want empty-response error, got %v", err)
	}
	if se, ok := err.(statusErr); !ok || se.StatusCode() != http.StatusBadGateway {
		t.Fatalf("want 502 statusErr, got %#v", err)
	}
}

func TestAntigravityEmptyStreamRetryCanBeDisabled(t *testing.T) {
	t.Setenv("ANTIGRAVITY_RETRY_EMPTY_STREAM", "off")
	chunks, err := runAntigravityRawStream(t, `data: {"response":{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}]}}

`)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("disabled: want passthrough, got chunks=%d err=%v", len(chunks), err)
	}
}

func TestAntigravityStreamWithContentFlushesHeldFrames(t *testing.T) {
	chunks, err := runAntigravityRawStream(t, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}

`)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(bytesJoin(chunks))
	if !strings.Contains(joined, "hello") || len(chunks) < 2 {
		t.Fatalf("want held frame + content, got %d chunks: %s", len(chunks), joined)
	}
}

func TestAntigravityStreamPayloadHasContent(t *testing.T) {
	cases := map[string]bool{
		`{"response":{"candidates":[{"content":{"parts":[{"text":""}]}}]}}`:                          false,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"x"}]}}]}}`:   false,
		`{"response":{"usageMetadata":{"promptTokenCount":2}}}`:                                      false,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"hm","thought":true}]}}]}}`:         true,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f"}}]}}]}}`:        true,
		`{"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`:                   true,
		`{"response":{"candidates":[{"finishReason":"SAFETY"}]}}`:                                    true,
		`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}}`:                       true,
		`{"candidates":[{"content":{"parts":[{"text":"plain"}]}}]}`:                                  true,
	}
	for in, want := range cases {
		if got := antigravityStreamPayloadHasContent([]byte(in)); got != want {
			t.Errorf("%s: got %v want %v", in, got, want)
		}
	}
}

func bytesJoin(b [][]byte) []byte {
	var out []byte
	for _, x := range b {
		out = append(out, x...)
	}
	return out
}
