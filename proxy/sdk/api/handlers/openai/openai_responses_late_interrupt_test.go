package openai

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// Interrupt diagnostics must exist even before the first create, and must not
// copy private extension fields, credentials, or raw client-controlled IDs.
func TestResponsesInterruptDiagnosticsAreSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.SDKConfig{RequestLog: true}
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
	timelines := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		handler.ResponsesWebsocket(c)
		body, _ := c.Get(wsTimelineBodyKey)
		data, _ := body.([]byte)
		timelines <- string(data)
	})
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			if errClose := client.Close(); errClose != nil {
				t.Errorf("close client: %v", errClose)
			}
		}
	}()
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"private-id-token","mode":"secret-mode","authorization":"Bearer secret-credential","input":"private-conversation"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errDeadline := client.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	_, payload, errRead := client.ReadMessage()
	if errRead != nil {
		t.Fatal(errRead)
	}
	if gjson.GetBytes(payload, "type").String() != "error" {
		t.Fatalf("unknown interrupt must still fail: %s", payload)
	}
	if errClose := client.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	clientClosed = true
	select {
	case timeline := <-timelines:
		for _, event := range []string{"Event: websocket.interrupt", "Event: websocket.interrupt.outcome", `"outcome":"rejected"`} {
			if !strings.Contains(timeline, event) {
				t.Errorf("missing %s in timeline: %s", event, timeline)
			}
		}
		for _, secret := range []string{"private-id-token", "secret-mode", "secret-credential", "private-conversation"} {
			if strings.Contains(timeline, secret) {
				t.Errorf("interrupt diagnostic leaked %s", secret)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeline was not finalized")
	}
}
