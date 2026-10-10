package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type capErr struct {
	code  int
	msg   string
	retry *time.Duration
}

func (e capErr) Error() string              { return e.msg }
func (e capErr) StatusCode() int            { return e.code }
func (e capErr) RetryAfter() *time.Duration { return e.retry }

const capacityBody = `{"error":{"code":503,"message":"No capacity available for model gemini-3.1-pro-low on the server","status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`

func TestIsCapacityError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"503 capacity", capErr{code: 503, msg: capacityBody}, true},
		{"503 unavailable", capErr{code: 503, msg: `{"error":{"status":"UNAVAILABLE"}}`}, true},
		{"429 capacity", capErr{code: 429, msg: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`}, true},
		{"429 quota", capErr{code: 429, msg: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`}, false},
		{"429 rate limit", capErr{code: 429, msg: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`}, false},
		{"400", capErr{code: 400, msg: "no capacity available"}, false},
		{"home unavailable", &Error{Code: "home_unavailable", Message: "unavailable", HTTPStatus: 503}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsCapacityError(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCapacityBackoff(t *testing.T) {
	s := CapacityRetrySettings{Enabled: true, MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 8 * time.Second}
	mid := func() float64 { return 0.5 } // no jitter
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 5: 8 * time.Second} {
		if got := capacityBackoff(s, attempt, nil, mid); got != want {
			t.Errorf("attempt %d: got %s want %s", attempt, got, want)
		}
	}
	lo := capacityBackoff(s, 2, nil, func() float64 { return 0 })
	hi := capacityBackoff(s, 2, nil, func() float64 { return 1 })
	if lo != 1600*time.Millisecond || hi != 2400*time.Millisecond {
		t.Errorf("jitter bounds %s..%s", lo, hi)
	}
	hint := 5 * time.Second
	if got := capacityBackoff(s, 1, &hint, mid); got != 5*time.Second {
		t.Errorf("RetryInfo hint not honoured: %s", got)
	}
	big := time.Minute
	if got := capacityBackoff(s, 1, &big, mid); got != 8*time.Second {
		t.Errorf("hint not capped: %s", got)
	}
}

func TestNewCapacityRetrySettings(t *testing.T) {
	if s := NewCapacityRetrySettings(nil, 0, "", ""); s != DefaultCapacityRetrySettings() {
		t.Fatalf("defaults: %+v", s)
	}
	off := false
	s := NewCapacityRetrySettings(&off, 2, "500ms", "100ms")
	if s.Enabled || s.MaxAttempts != 2 || s.BaseDelay != 500*time.Millisecond || s.MaxDelay != 500*time.Millisecond {
		t.Fatalf("parsed: %+v", s)
	}
}

func stubCapacitySleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	prev := capacitySleep
	capacitySleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	t.Cleanup(func() { capacitySleep = prev })
	return &slept
}

func TestWithCapacityRetryHelper(t *testing.T) {
	slept := stubCapacitySleep(t)
	s := DefaultCapacityRetrySettings()
	calls := 0
	got, err := withCapacityRetry(context.Background(), s, "m", func() (int, error) {
		calls++
		if calls < 3 {
			return 0, capErr{code: 503, msg: capacityBody}
		}
		return 42, nil
	})
	if err != nil || got != 42 || calls != 3 || len(*slept) != 2 {
		t.Fatalf("got=%d err=%v calls=%d slept=%v", got, err, calls, *slept)
	}

	// gives up after MaxAttempts and returns the last capacity error
	calls = 0
	_, err = withCapacityRetry(context.Background(), s, "m", func() (int, error) { calls++; return 0, capErr{code: 503, msg: capacityBody} })
	if err == nil || calls != 1+s.MaxAttempts {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	// non-capacity errors and disabled settings are not retried
	calls = 0
	_, _ = withCapacityRetry(context.Background(), s, "m", func() (int, error) { calls++; return 0, capErr{code: 429, msg: "QUOTA_EXHAUSTED"} })
	off := s
	off.Enabled = false
	_, _ = withCapacityRetry(context.Background(), off, "m", func() (int, error) { calls++; return 0, capErr{code: 503, msg: capacityBody} })
	if calls != 2 {
		t.Fatalf("unexpected retries: %d", calls)
	}

	// a cancelled request stops retrying
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_, _ = withCapacityRetry(ctx, s, "m", func() (int, error) { calls++; return 0, capErr{code: 503, msg: capacityBody} })
	if calls != 1 {
		t.Fatalf("retried after cancel: %d", calls)
	}
}

// capacityExecutor fails with a capacity error for the first `failures` calls
// (per auth listed in alwaysFail: forever) and records the model of every call.
type capacityExecutor struct {
	mu         sync.Mutex
	failures   int
	alwaysFail map[string]bool
	calls      []string // authID:model
}

func (e *capacityExecutor) Identifier() string { return "antigravity" }

func (e *capacityExecutor) attempt(auth *Auth, model string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID+":"+model)
	if e.alwaysFail[auth.ID] {
		return capErr{code: 503, msg: capacityBody}
	}
	if e.failures > 0 {
		e.failures--
		return capErr{code: 503, msg: capacityBody}
	}
	return nil
}

func (e *capacityExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.attempt(auth, req.Model); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte("ok:" + req.Model)}, nil
}

func (e *capacityExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.attempt(auth, req.Model); err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("stream:" + req.Model)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: ch}, nil
}

func (e *capacityExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (e *capacityExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unsupported")
}
func (e *capacityExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unsupported")
}

func newCapacityManager(t *testing.T, exec *capacityExecutor, model string, authIDs ...string) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)
	for _, id := range authIDs {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		id := id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "antigravity"}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestManagerCapacityRetrySameModel(t *testing.T) {
	const model = "gemini-3.1-pro-low"
	slept := stubCapacitySleep(t)
	exec := &capacityExecutor{failures: 2}
	m := newCapacityManager(t, exec, model, "cap-a")
	resp, err := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v (calls %v)", err, exec.calls)
	}
	if string(resp.Payload) != "ok:"+model {
		t.Fatalf("payload %q", resp.Payload)
	}
	if len(*slept) != 2 {
		t.Fatalf("backoffs = %v, want 2", *slept)
	}
	for _, c := range exec.calls {
		if c != "cap-a:"+model {
			t.Fatalf("model changed or wrong auth: %v", exec.calls)
		}
	}
}

func TestManagerCapacityRetryStreamBeforeFirstByte(t *testing.T) {
	const model = "gemini-3.1-pro-low"
	slept := stubCapacitySleep(t)
	exec := &capacityExecutor{failures: 1}
	m := newCapacityManager(t, exec, model, "cap-s")
	res, err := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var got string
	for ch := range res.Chunks {
		got += string(ch.Payload)
	}
	if got != "stream:"+model || len(*slept) != 1 {
		t.Fatalf("got %q slept %v calls %v", got, *slept, exec.calls)
	}
}

func TestManagerCapacityPrefersOtherCredential(t *testing.T) {
	const model = "gemini-3.1-pro-low"
	slept := stubCapacitySleep(t)
	exec := &capacityExecutor{alwaysFail: map[string]bool{"cap-x": true}}
	m := newCapacityManager(t, exec, model, "cap-x", "cap-y")
	for i := 0; i < 3; i++ {
		if _, err := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			t.Fatalf("execute %d: %v (calls %v)", i, err, exec.calls)
		}
	}
	if len(*slept) != 0 {
		t.Fatalf("should switch credential without backoff, slept %v", *slept)
	}
}

func TestManagerCapacityRetryDisabled(t *testing.T) {
	const model = "gemini-3.1-pro-low"
	slept := stubCapacitySleep(t)
	exec := &capacityExecutor{failures: 1}
	m := newCapacityManager(t, exec, model, "cap-off")
	m.SetCapacityRetry(CapacityRetrySettings{Enabled: false})
	if _, err := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err == nil || !IsCapacityError(err) {
		t.Fatalf("want capacity error, got %v", err)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept %v", *slept)
	}
}
