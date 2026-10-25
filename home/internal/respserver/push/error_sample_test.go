package push

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/errorsamples"
)

func TestCapErrorSample(t *testing.T) {
	obj := map[string]any{"request_body": strings.Repeat("a", 10000), "response": "short", "upstream_request": strings.Repeat("b", 3000)}
	CapErrorSample(obj, 5000)
	if obj["response"] != "short" || !strings.Contains(obj["request_body"].(string), "truncated") {
		t.Fatalf("bad cap %v", obj["truncated"])
	}
	total := len(obj["request_body"].(string)) + len(obj["upstream_request"].(string))
	if total > 5200 {
		t.Fatalf("total %d", total)
	}
}

func TestErrorSampleKeep(t *testing.T) {
	errorsamples.Set(nil, 0, 0, 0, "499", "429=2,503:no-capacity=1")
	s := errorsamples.Get()
	if s.Keep("499:client-cancel") != 0 || s.Keep("429:quota") != 2 || s.Keep("503:no-capacity") != 1 || s.Keep("400:x") != 5 {
		t.Fatal("keep wrong")
	}
	f := false
	errorsamples.Set(&f, 0, 0, 0, "", "")
	if errorsamples.Get().Keep("400") != 0 {
		t.Fatal("disabled")
	}
	errorsamples.Set(nil, 0, 0, 0, "", "")
}
