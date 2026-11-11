package helps

import "testing"

func TestAddPromptRewrittenMergesWithRewriteHits(t *testing.T) {
	r := &UsageReporter{}
	r.SetPromptNote("prompt_rewritten: app-global-chat-rules")
	r.AddPromptRewritten("sensitive-words")
	if r.promptNote != "prompt_rewritten: app-global-chat-rules,sensitive-words" {
		t.Fatalf("got %q", r.promptNote)
	}
	// dedupe
	r.AddPromptRewritten("sensitive-words")
	if r.promptNote != "prompt_rewritten: app-global-chat-rules,sensitive-words" {
		t.Fatalf("dedupe failed: %q", r.promptNote)
	}
}

func TestAddPromptRewrittenFromEmpty(t *testing.T) {
	r := &UsageReporter{}
	r.AddPromptRewritten("sensitive-words-replace")
	if r.promptNote != "prompt_rewritten: sensitive-words-replace" {
		t.Fatalf("got %q", r.promptNote)
	}
}
