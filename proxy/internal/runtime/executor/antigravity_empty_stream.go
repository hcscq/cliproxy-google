package executor

import (
	"os"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	antigravityEmptyStreamMessage   = "upstream returned an empty response (no output)"
	antigravityEmptyStreamHoldLimit = 32
)

// antigravityRetryEmptyStreamEnabled reports whether content-free upstream
// streams are retried on another credential (ANTIGRAVITY_RETRY_EMPTY_STREAM,
// default on).
func antigravityRetryEmptyStreamEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ANTIGRAVITY_RETRY_EMPTY_STREAM"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// antigravityStreamPayloadHasContent reports whether an upstream frame carries
// anything the client must see: a non-empty part (text, thought, function
// call, inline data, ...), a block reason, or a non-STOP finish reason.
func antigravityStreamPayloadHasContent(payload []byte) bool {
	root := gjson.ParseBytes(payload)
	if r := root.Get("response"); r.Exists() && r.IsObject() {
		root = r
	}
	if root.Get("promptFeedback.blockReason").String() != "" {
		return true
	}
	found := false
	root.Get("candidates").ForEach(func(_, cand gjson.Result) bool {
		if fr := strings.ToUpper(strings.TrimSpace(cand.Get("finishReason").String())); fr != "" && fr != "STOP" && fr != "FINISH_REASON_UNSPECIFIED" {
			found = true
			return false
		}
		cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			part.ForEach(func(k, v gjson.Result) bool {
				switch k.String() {
				case "text":
					if v.String() != "" {
						found = true
					}
				case "thought", "thoughtSignature":
				default:
					found = true
				}
				return !found
			})
			return !found
		})
		return !found
	})
	return found
}
