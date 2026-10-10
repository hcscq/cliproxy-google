package test

import (
	"encoding/json"
	"strings"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
)

// terminalStreamErrorDetail returns the error object of the last "error" or
// "response.failed" frame in an SSE body.
func terminalStreamErrorDetail(t *testing.T, body string) map[string]any {
	t.Helper()
	var detail map[string]any
	for _, frame := range strings.Split(body, "\n\n") {
		var data string
		for _, line := range strings.Split(frame, "\n") {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				data = rest
			}
		}
		if data == "" {
			continue
		}
		var payload map[string]any
		if errUnmarshal := json.Unmarshal([]byte(data), &payload); errUnmarshal != nil {
			continue
		}
		switch payload["type"] {
		case "error":
			if errorObj, ok := payload["error"].(map[string]any); ok {
				detail = errorObj
			}
		case "response.failed":
			if response, ok := payload["response"].(map[string]any); ok {
				if errorObj, ok := response["error"].(map[string]any); ok {
					detail = errorObj
				}
			}
		}
	}
	if detail == nil {
		t.Fatalf("no terminal error frame in SSE body:\n%s", body)
	}
	return detail
}
