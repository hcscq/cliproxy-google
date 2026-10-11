package affinity

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var long = strings.Repeat("reference data line. ", 200)

func TestComputeSourcesAndStability(t *testing.T) {
	cfg := config.StickySessionsConfig{}
	h := http.Header{}
	// Prefix: a continued conversation keeps the key (system + first user msg).
	t1 := `{"model":"m","messages":[{"role":"system","content":"` + long + `"},{"role":"user","content":"q1"}]}`
	t2 := `{"model":"m","messages":[{"role":"system","content":"` + long + `"},{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"}]}`
	k1, s1 := Compute(cfg, h, []byte(t1), "m")
	k2, _ := Compute(cfg, h, []byte(t2), "m")
	if s1 != SourcePrefix || k1 == "" || k1 != k2 {
		t.Fatalf("prefix keys %q %q src=%s", k1, k2, s1)
	}
	if k3, _ := Compute(cfg, h, []byte(t1), "other-model"); k3 == k1 {
		t.Fatal("key must depend on model")
	}
	// Same text in Claude and Gemini shapes also yields a prefix key.
	claude := `{"system":"` + long + `","messages":[{"role":"user","content":[{"type":"text","text":"q1"}]}]}`
	gem := `{"systemInstruction":{"parts":[{"text":"` + long + `"}]},"contents":[{"role":"user","parts":[{"text":"q1"}]}]}`
	for _, b := range []string{claude, gem} {
		if k, s := Compute(cfg, h, []byte(b), "m"); k == "" || s != SourcePrefix {
			t.Fatalf("no prefix key for %s", b[:20])
		}
	}
	// Short prompts get no key (avoid hot-spotting).
	if k, _ := Compute(cfg, h, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "m"); k != "" {
		t.Fatal("short prompt should not get a key")
	}
	// Explicit sources win in priority order.
	h.Set("X-Session-Id", "sess-1")
	if _, s := Compute(cfg, h, []byte(t1), "m"); s != SourceHeader {
		t.Fatalf("header source = %s", s)
	}
	h = http.Header{}
	if _, s := Compute(cfg, h, []byte(`{"prompt_cache_key":"pc","user":"u","messages":[]}`), "m"); s != SourcePromptCache {
		t.Fatalf("got %s", s)
	}
	if _, s := Compute(cfg, h, []byte(`{"user":"u","messages":[]}`), "m"); s != SourceOpenAIUser {
		t.Fatalf("got %s", s)
	}
	if _, s := Compute(cfg, h, []byte(`{"system":"x","metadata":{"user_id":"u1"},"messages":[]}`), "m"); s != SourceClaudeMeta {
		t.Fatalf("got %s", s)
	}
	if _, s := Compute(cfg, h, []byte(`{"cachedContent":"cachedContents/abc","contents":[]}`), "m"); s != SourceGeminiCache {
		t.Fatalf("got %s", s)
	}
	// Source list restricts; disabled returns nothing.
	if k, _ := Compute(config.StickySessionsConfig{KeySources: []string{SourceHeader}}, h, []byte(t1), "m"); k != "" {
		t.Fatal("prefix disabled by key-sources")
	}
	off := false
	if k, _ := Compute(config.StickySessionsConfig{Enabled: &off}, h, []byte(t1), "m"); k != "" {
		t.Fatal("disabled")
	}
}
