package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const cachedUsageResp = `{"response":{"responseId":"r1","modelVersion":"gemini-3-flash","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":28439,"candidatesTokenCount":5,"cachedContentTokenCount":24544,"totalTokenCount":28444}}}`

func TestClaudeNonStreamUsageExcludesCacheReads(t *testing.T) {
	out := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "", []byte(`{"model":"gemini-3-flash"}`), nil, []byte(cachedUsageResp), nil)
	u := gjson.GetBytes(out, "usage")
	if u.Get("input_tokens").Int() != 28439-24544 || u.Get("cache_read_input_tokens").Int() != 24544 {
		t.Fatalf("usage = %s", u.Raw)
	}
	if !u.Get("cache_creation_input_tokens").Exists() || u.Get("cache_creation_input_tokens").Int() != 0 {
		t.Fatalf("cache_creation_input_tokens missing: %s", u.Raw)
	}
	// No cache hit: both cache fields still present as 0.
	plain := strings.Replace(cachedUsageResp, `"cachedContentTokenCount":24544,`, "", 1)
	u = gjson.GetBytes(ConvertAntigravityResponseToClaudeNonStream(context.Background(), "", []byte(`{}`), nil, []byte(plain), nil), "usage")
	if u.Get("input_tokens").Int() != 28439 || !u.Get("cache_read_input_tokens").Exists() || !u.Get("cache_creation_input_tokens").Exists() {
		t.Fatalf("usage without cache = %s", u.Raw)
	}
}

func TestClaudeStreamMessageStartExcludesCacheReads(t *testing.T) {
	var param any
	first := []byte(`{"response":{"responseId":"r1","modelVersion":"gemini-3-flash","candidates":[{"content":{"role":"model","parts":[{"text":"o"}]}}],"cpaUsageMetadata":{"promptTokenCount":28439,"cachedContentTokenCount":24544}}}`)
	var all strings.Builder
	for _, c := range ConvertAntigravityResponseToClaude(context.Background(), "", []byte(`{"model":"gemini-3-flash"}`), nil, first, &param) {
		all.Write(c)
	}
	for _, c := range ConvertAntigravityResponseToClaude(context.Background(), "", []byte(`{"model":"gemini-3-flash"}`), nil, []byte(cachedUsageResp), &param) {
		all.Write(c)
	}
	var start, delta gjson.Result
	for _, line := range strings.Split(all.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		ev := gjson.Parse(strings.TrimSpace(line[5:]))
		switch ev.Get("type").String() {
		case "message_start":
			start = ev.Get("message.usage")
		case "message_delta":
			delta = ev.Get("usage")
		}
	}
	if start.Get("input_tokens").Int() != 3895 || start.Get("cache_read_input_tokens").Int() != 24544 {
		t.Fatalf("message_start usage = %s", start.Raw)
	}
	if delta.Get("input_tokens").Int() != 3895 || delta.Get("cache_read_input_tokens").Int() != 24544 || !delta.Get("cache_creation_input_tokens").Exists() {
		t.Fatalf("message_delta usage = %s", delta.Raw)
	}
}
