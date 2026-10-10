package gemini

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// WriteErrorResponse writes errors in the Gemini API shape
// {"error":{"code":N,"message":"...","status":"..."}} so Google SDKs can parse
// them. Upstream Gemini errors that already have this shape pass through
// unchanged; local errors (unknown model, cooldown, ...) are converted from the
// generic OpenAI-style shape.
func (h *GeminiAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	if msg == nil || msg.DirectResponse {
		h.BaseAPIHandler.WriteErrorResponse(c, msg)
		return
	}
	status := http.StatusInternalServerError
	if msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	errText := ""
	if msg.Error != nil {
		errText = strings.TrimSpace(msg.Error.Error())
		for _, value := range coreauth.SafeResponseHeaders(msg.Error).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
	}
	h.BaseAPIHandler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode:     status,
		Error:          msg.Error,
		Addon:          msg.Addon,
		DirectResponse: true,
		Body:           geminiErrorBody(status, errText),
		Headers:        http.Header{"Content-Type": []string{"application/json"}},
	})
}

func geminiErrorBody(status int, errText string) []byte {
	if errText != "" && json.Valid([]byte(errText)) {
		var parsed struct {
			Error map[string]any `json:"error"`
		}
		if json.Unmarshal([]byte(errText), &parsed) == nil && parsed.Error != nil {
			if _, hasStatus := parsed.Error["status"].(string); hasStatus {
				if _, numeric := parsed.Error["code"].(float64); numeric {
					return []byte(errText) // already Gemini-shaped (upstream)
				}
			}
			if m, ok := parsed.Error["message"].(string); ok && m != "" {
				errText = m
			}
		}
	}
	if errText == "" {
		errText = http.StatusText(status)
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": errText, "status": googleRPCStatus(status)}})
	return body
}

func googleRPCStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return "DEADLINE_EXCEEDED"
	default:
		if status >= 500 {
			return "INTERNAL"
		}
		return "FAILED_PRECONDITION"
	}
}
