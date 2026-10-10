package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAIStudioCountPayloadAfterCleanup(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"generationConfig.temperature": 0.2, "tools": []any{map[string]any{"googleSearch": map[string]any{}}}, "safetySettings": []any{map[string]any{"category": "configured"}}},
	}}}}
	req := core.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), Metadata: map[string]any{"action": "countTokens"}}
	connected := make(chan struct{})
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) { return "count-test", nil },
		OnConnected:     func(string) { close(connected) },
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Error(errStop)
		}
	}()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, configured := range []bool{false, true} {
		selected := &config.Config{}
		if configured {
			selected = cfg
		}
		captured := make(chan []byte, 1)
		clientErrors := make(chan error, 1)
		go func() {
			var message wsrelay.Message
			if errRead := conn.ReadJSON(&message); errRead != nil {
				clientErrors <- errRead
				return
			}
			captured <- []byte(message.Payload["body"].(string))
			clientErrors <- conn.WriteJSON(wsrelay.Message{ID: message.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": float64(http.StatusOK), "headers": map[string]any{"Content-Type": "application/json"}, "body": `{"totalTokens":1}`}})
		}()
		executor := NewAIStudioExecutor(selected, "aistudio", relay)
		if _, errCount := executor.CountTokens(ctx, &cliproxyauth.Auth{ID: "count-test", Provider: "aistudio"}, req, core.Options{SourceFormat: sdktranslator.FormatGemini}); errCount != nil {
			t.Fatal(errCount)
		}
		if errClient := <-clientErrors; errClient != nil {
			t.Fatal(errClient)
		}
		body := <-captured
		for _, field := range []string{"generationConfig", "tools", "safetySettings"} {
			if gjson.GetBytes(body, field).Exists() != configured {
				t.Fatalf("count cleanup precedence for %s: %s", field, body)
			}
		}
	}
}
