package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestStreamThinkingEmulationApplies(t *testing.T) {
	withThoughts := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`)
	noThoughts := []byte(`{"request":{"generationConfig":{}}}`)
	off := &config.Config{}
	on := &config.Config{StreamThinkingEmulation: true}
	cases := []struct {
		cfg   *config.Config
		model string
		body  []byte
		want  bool
	}{
		{off, "gemini-3-flash", withThoughts, false},
		{nil, "gemini-3-flash", withThoughts, false},
		{on, "gemini-3-flash", withThoughts, true},
		{on, "gemini-3-flash", noThoughts, false},
		{on, "claude-sonnet-4-6", withThoughts, false},
	}
	for _, c := range cases {
		if got := streamThinkingEmulationApplies(c.cfg, c.model, c.body); got != c.want {
			t.Errorf("%s: got %v want %v", c.model, got, c.want)
		}
	}
}

func TestReplayNonStreamAsSSE(t *testing.T) {
	body := "{\n  \"response\": {\"candidates\": [{\"content\": {\"parts\": [{\"thought\": true, \"text\": \"a\\nb\"}, {\"text\": \"ok\"}]}}]}\n}\n"
	out, err := replayNonStreamAsSSE(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "data: {") || !strings.HasSuffix(s, "}\n\n") || strings.Count(s, "\n") != 2 {
		t.Fatalf("bad SSE: %q", s)
	}
	if _, err := replayNonStreamAsSSE(strings.NewReader("{bad")); err == nil {
		t.Fatal("want error on invalid JSON")
	}
}
