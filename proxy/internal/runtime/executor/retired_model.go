package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// Retired Antigravity models (e.g. gemini-3-flash-agent, gemini-3.5-flash-low)
// still answer 200 with a canned text such as "Gemini 3.5 Flash is no longer
// available. Please switch to Gemini 3.7 Flash in the model picker." Passing
// that through looks like a real answer to clients, so it is turned into a
// model-not-found error (404, reason MODEL_RETIRED / model_not_supported). The
// conductor treats it as a model-scoped failure, so only that model on that
// credential is affected.

var retiredModelPattern = regexp.MustCompile(`(?i)\bis no longer available\b[^.]*\.\s*(please\s+)?switch to\b`)

const retiredModelMaxTextLen = 600

// retiredModelText returns the canned retirement text when payload (an
// Antigravity response or stream chunk) consists only of it.
func retiredModelText(payload []byte) (string, bool) {
	root := gjson.ParseBytes(payload)
	parts := root.Get("response.candidates.0.content.parts")
	if !parts.Exists() {
		parts = root.Get("candidates.0.content.parts")
	}
	if !parts.IsArray() {
		return "", false
	}
	var text strings.Builder
	for _, part := range parts.Array() {
		if part.Get("functionCall").Exists() || part.Get("inlineData").Exists() {
			return "", false
		}
		if part.Get("thought").Bool() {
			continue
		}
		text.WriteString(part.Get("text").String())
	}
	t := strings.TrimSpace(text.String())
	if t == "" || len(t) > retiredModelMaxTextLen || !retiredModelPattern.MatchString(t) {
		return "", false
	}
	return t, true
}

// retiredModelErr builds the client-facing error for a retired model.
func retiredModelErr(model, upstreamText string) statusErr {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    http.StatusNotFound,
			"message": "model " + model + " has been retired upstream (model_not_supported): " + upstreamText,
			"status":  "NOT_FOUND",
			"type":    "invalid_request_error",
			"param":   "model",
			"details": []map[string]any{{
				"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "MODEL_RETIRED",
				"domain": "cliproxy",
				"metadata": map[string]string{
					"model":            model,
					"upstream_message": upstreamText,
				},
			}},
		},
	})
	return newAntigravityStatusErr(http.StatusNotFound, body)
}

const retiredPeekLimit = 256 << 10

// peekRetiredModelStream buffers the start of an SSE body until it can decide
// whether the response is a retirement notice: any thought, function call or
// inline data, or 80+ characters of text without "no longer available", means
// a real answer and the buffered bytes are replayed unchanged. Otherwise the
// (short) stream is read to the end and checked against the notice pattern.
func peekRetiredModelStream(body io.ReadCloser, model string) (io.ReadCloser, error) {
	reader := bufio.NewReaderSize(body, 64<<10)
	var consumed bytes.Buffer
	var text strings.Builder
	decided := false
	for !decided && consumed.Len() < retiredPeekLimit {
		line, errRead := reader.ReadBytes('\n')
		consumed.Write(line)
		if payload := helps.JSONPayload(line); payload != nil && gjson.ValidBytes(payload) {
			parts := gjson.GetBytes(payload, "response.candidates.0.content.parts")
			if !parts.Exists() {
				parts = gjson.GetBytes(payload, "candidates.0.content.parts")
			}
			for _, part := range parts.Array() {
				if part.Get("thought").Bool() || part.Get("functionCall").Exists() || part.Get("inlineData").Exists() {
					decided = true
					break
				}
				text.WriteString(part.Get("text").String())
			}
			t := strings.TrimSpace(text.String())
			lowerT := strings.ToLower(t)
			switch {
			case decided:
			case t != "" && !strings.HasPrefix(lowerT, "gemini") && !strings.HasPrefix("gemini", lowerT):
				// Notices start with the model's display name ("Gemini ...").
				decided = true
			case len(t) > retiredModelMaxTextLen, len(t) >= 80 && !strings.Contains(lowerT, "no longer available"):
				decided = true
			case gjson.GetBytes(payload, "response.candidates.0.finishReason").String() != "" || gjson.GetBytes(payload, "candidates.0.finishReason").String() != "":
				if retiredModelPattern.MatchString(t) {
					return nil, retiredModelErr(model, t)
				}
				decided = true
			}
		}
		if errRead != nil {
			if !decided {
				if t := strings.TrimSpace(text.String()); t != "" && retiredModelPattern.MatchString(t) {
					return nil, retiredModelErr(model, t)
				}
			}
			break
		}
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(consumed.Bytes()), reader), body}, nil
}
