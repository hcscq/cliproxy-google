package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const presearchGM = `{"webSearchQueries":["latest linux kernel"],"groundingChunks":[{"web":{"title":"kernel.org","uri":"https://www.kernel.org/"}},{"web":{"title":"phoronix","uri":"https://www.phoronix.com/x"}}]}`

func TestPresearchRenderedNonStream(t *testing.T) {
	orig := []byte(`{"messages":[{"role":"user","content":"q"}],"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Bash","input_schema":{"type":"object"}}]}`)
	body := []byte(`{"response":{"cpaPresearchGrounding":` + presearchGM + `,"candidates":[{"content":{"role":"model","parts":[{"text":"Kernel 7.3."},{"functionCall":{"name":"Bash","args":{"command":"uname -r"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`)
	out := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "", orig, []byte(`{"model":"gemini-2.5-flash"}`), body, nil)
	c := gjson.GetBytes(out, "content").Array()
	if len(c) < 4 || c[0].Get("type").String() != "server_tool_use" || c[1].Get("type").String() != "web_search_tool_result" {
		t.Fatalf("want server_tool_use + web_search_tool_result first: %s", out)
	}
	if c[0].Get("input.query").String() != "latest linux kernel" || c[1].Get("tool_use_id").String() != c[0].Get("id").String() {
		t.Fatalf("blocks not linked: %s", out)
	}
	if n := len(c[1].Get("content").Array()); n != 2 || c[1].Get("content.0.url").String() != "https://www.kernel.org/" {
		t.Fatalf("results: %s", out)
	}
	if c[2].Get("type").String() != "text" || c[3].Get("type").String() != "tool_use" || gjson.GetBytes(out, "stop_reason").String() != "tool_use" {
		t.Fatalf("pass-2 content must follow unchanged: %s", out)
	}
	if gjson.GetBytes(out, "usage.server_tool_use.web_search_requests").Int() != 1 {
		t.Fatalf("web_search_requests: %s", out)
	}
	if strings.Contains(string(out), "cpaPresearchGrounding") {
		t.Fatal("marker leaked")
	}
}

func TestPresearchRenderedStreamFirst(t *testing.T) {
	orig := []byte(`{"messages":[{"role":"user","content":"q"}],"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Bash","input_schema":{"type":"object"}}]}`)
	req := []byte(`{"model":"gemini-2.5-flash","request":{"tools":[{"functionDeclarations":[{"name":"Bash"}]}]}}`)
	frames := []string{
		`{"response":{"cpaPresearchGrounding":` + presearchGM + `,"candidates":[{"content":{"role":"model","parts":[{"text":"think","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Kernel 7.3."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`,
	}
	var param any
	var all strings.Builder
	for _, f := range frames {
		for _, ch := range ConvertAntigravityResponseToClaude(context.Background(), "", orig, req, []byte(f), &param) {
			all.Write(ch)
		}
	}
	for _, ch := range ConvertAntigravityResponseToClaude(context.Background(), "", orig, req, []byte("[DONE]"), &param) {
		all.Write(ch)
	}
	s := all.String()
	var starts []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data:") {
			e := gjson.Parse(strings.TrimSpace(line[5:]))
			if e.Get("type").String() == "content_block_start" {
				starts = append(starts, e.Get("content_block.type").String()+"@"+e.Get("index").String())
			}
		}
	}
	want := []string{"server_tool_use@0", "web_search_tool_result@1", "thinking@2", "text@3"}
	if strings.Join(starts, ",") != strings.Join(want, ",") {
		t.Fatalf("block order = %v, want %v\n%s", starts, want, s)
	}
	if !strings.Contains(s, `"web_search_requests":1`) || strings.Contains(s, "cpaPresearchGrounding") {
		t.Fatalf("usage/marker: %s", s)
	}
}
