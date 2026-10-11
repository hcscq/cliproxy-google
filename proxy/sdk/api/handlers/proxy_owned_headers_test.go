package handlers

import (
	"net/http"
	"testing"
)

func TestThinkingAdjustedHeaderSurvivesDisabledPassthrough(t *testing.T) {
	raw := http.Header{"X-Thinking-Adjusted": {"sent budget=0"}, "X-Upstream-Secret": {"x"}}
	for name, got := range map[string]http.Header{
		"executor":     downstreamHeadersFromExecutor(raw, false),
		"interceptors": downstreamHeadersAfterInterceptors(raw, raw, false),
	} {
		if got.Get("X-Thinking-Adjusted") != "sent budget=0" {
			t.Errorf("%s: header dropped: %v", name, got)
		}
		if got.Get("X-Upstream-Secret") != "" {
			t.Errorf("%s: upstream header leaked without passthrough: %v", name, got)
		}
	}
	if h := downstreamHeadersFromExecutor(http.Header{"X-Other": {"1"}}, false); len(h) != 0 {
		t.Errorf("no proxy-owned header should give no headers, got %v", h)
	}
}
