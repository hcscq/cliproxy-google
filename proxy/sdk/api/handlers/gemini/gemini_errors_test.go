package gemini

import (
	"encoding/json"
	"testing"
)

func TestGeminiErrorBody(t *testing.T) {
	var got struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	body := geminiErrorBody(404, `{"error":{"message":"model_not_found: model x does not exist","type":"invalid_request_error","code":"model_not_found"}}`)
	if err := json.Unmarshal(body, &got); err != nil || got.Error.Code != 404 || got.Error.Status != "NOT_FOUND" || got.Error.Message != "model_not_found: model x does not exist" {
		t.Fatalf("converted: %s", body)
	}
	up := `{"error":{"code":503,"message":"No capacity","status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`
	if string(geminiErrorBody(503, up)) != up {
		t.Fatal("upstream gemini error must pass through")
	}
	body = geminiErrorBody(429, "model_cooldown: all credentials cooling down")
	if err := json.Unmarshal(body, &got); err != nil || got.Error.Status != "RESOURCE_EXHAUSTED" || got.Error.Code != 429 {
		t.Fatalf("plain: %s", body)
	}
}
