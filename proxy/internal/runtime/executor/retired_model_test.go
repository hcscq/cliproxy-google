package executor

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const retiredNotice = "Gemini 3.5 Flash is no longer available. Please switch to Gemini 3.7 Flash in the model picker."

func sseLine(text string) string {
	return `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonString(text) + `}]}}]}}` + "\n\n"
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func TestRetiredModelTextNonStream(t *testing.T) {
	body := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"` + retiredNotice + `"}]},"finishReason":"STOP"}]}}`)
	text, ok := retiredModelText(body)
	if !ok || text != retiredNotice {
		t.Fatalf("not detected: %q %v", text, ok)
	}
	err := retiredModelErr("gemini-3.5-flash-low", text)
	if err.StatusCode() != http.StatusNotFound || !strings.Contains(err.Error(), "model_not_supported") || !strings.Contains(err.Error(), "MODEL_RETIRED") {
		t.Fatalf("bad error: %d %s", err.StatusCode(), err.Error())
	}
	for _, normal := range []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello! How can I help?"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"The old API is no longer available. Please switch to v2."},{"functionCall":{"name":"x"}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("long answer ", 80) + ` is no longer available. Please switch to y"}]}}]}}`,
	} {
		if _, ok := retiredModelText([]byte(normal)); ok {
			t.Fatalf("false positive: %s", normal[:60])
		}
	}
}

func TestPeekRetiredModelStreamSplitChunks(t *testing.T) {
	stream := sseLine("Gemini 3.5 Flash is no longer") + sseLine(" available. Please switch to Gemini 3.7 Flash in the model picker.")
	_, err := peekRetiredModelStream(io.NopCloser(strings.NewReader(stream)), "gemini-3.5-flash-low")
	if err == nil || !strings.Contains(err.Error(), "MODEL_RETIRED") {
		t.Fatalf("expected retired error, got %v", err)
	}
}

func TestPeekRetiredModelStreamPassesThroughNormal(t *testing.T) {
	cases := []string{
		sseLine("Hello") + sseLine(" world, this is a perfectly normal streaming answer that keeps going on and on."),
		`data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"thinking"}]}}]}}` + "\n\n" + sseLine("ok"),
		sseLine("short"),
	}
	for _, stream := range cases {
		rc, err := peekRetiredModelStream(io.NopCloser(strings.NewReader(stream)), "m")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _ := io.ReadAll(rc)
		if string(got) != stream {
			t.Fatalf("stream not replayed intact:\n%q\n%q", got, stream)
		}
	}
}

// A stream that stays open after its first chunk must not block the peek.
func TestPeekRetiredModelStreamDoesNotBlockOnOpenStream(t *testing.T) {
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte(sseLine("Hi"))) }()
	done := make(chan error, 1)
	go func() {
		_, err := peekRetiredModelStream(pr, "m")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peek blocked on open stream")
	}
	_ = pw.Close()
}

func TestPeekRetiredModelStreamFinishReasonDecides(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"` + retiredNotice + `"}]},"finishReason":"STOP"}]}}` + "\n\n"))
	}()
	done := make(chan error, 1)
	go func() {
		_, err := peekRetiredModelStream(pr, "gemini-3-flash-agent")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "MODEL_RETIRED") {
			t.Fatalf("want retired error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peek blocked")
	}
	_ = pw.Close()
}
