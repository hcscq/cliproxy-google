package chat_completions

import "testing"

func TestOpenAIFinishReasonFromGemini(t *testing.T) {
	for in, want := range map[string]string{"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "OTHER": "stop"} {
		if got := openAIFinishReasonFromGemini(in); got != want {
			t.Fatalf("%s -> %s, want %s", in, got, want)
		}
	}
}
