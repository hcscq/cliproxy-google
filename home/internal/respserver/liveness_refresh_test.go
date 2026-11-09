package respserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestRefreshSubscriptionLivenessRetriesLockTimeout(t *testing.T) {
	restore := zeroSubscriptionLivenessRetryDelay(t)
	defer restore()

	handler := &scriptedLivenessHandler{errs: []error{errors.New("canceling statement due to lock timeout"), nil}}
	server := New("", nil)
	server.SetClusterHandler(handler)
	lifetime := cluster.ConnectionLifetime{Fingerprint: "fp-retry", ConnectedAt: time.Unix(10, 0), Subscription: true}
	if errRefresh := server.refreshSubscriptionLiveness(context.Background(), lifetime); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if calls := handler.calls(); calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestRefreshSubscriptionLivenessDoesNotRetryTerminalError(t *testing.T) {
	handler := &scriptedLivenessHandler{errs: []error{errors.New("membership is not active")}}
	server := New("", nil)
	server.SetClusterHandler(handler)
	lifetime := cluster.ConnectionLifetime{Fingerprint: "fp-terminal", ConnectedAt: time.Unix(11, 0), Subscription: true}
	if errRefresh := server.refreshSubscriptionLiveness(context.Background(), lifetime); errRefresh == nil {
		t.Fatal("terminal liveness error was swallowed")
	}
	if calls := handler.calls(); calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestSubscriptionLockTimeoutDoesNotFenceWhenRetrySucceeds(t *testing.T) {
	restore := zeroSubscriptionLivenessRetryDelay(t)
	defer restore()

	connection := newSubscriptionTestConn()
	lifetime := cluster.ConnectionLifetime{Fingerprint: "fp-lock-retry", ConnectedAt: time.Unix(20, 0), Subscription: true}
	registry := NewFingerprintRegistry()
	tracked, errAccept := registry.Accept(context.Background(), connection, lifetime)
	if errAccept != nil {
		t.Fatal(errAccept)
	}
	defer func() { _ = tracked.Close() }()

	handler := &scriptedLivenessHandler{errs: []error{errors.New("ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)")}}
	server := New("", nil)
	server.fingerprints = registry
	server.SetClusterHandler(handler)
	cancel := server.startSubscriptionUpdates(tracked.Context(), tracked, newSafeWriter(connection))
	defer cancel()

	waitForSubscriptionOutput(t, connection, "pong", "subscription pong after lock-timeout retry")
	select {
	case <-tracked.Context().Done():
		t.Fatal("subscription was fenced after a lock timeout that succeeded on retry")
	default:
	}
	if calls := handler.calls(); calls < 2 {
		t.Fatalf("refresh calls = %d, want the failed attempt and the successful retry", calls)
	}
}

func TestSubscriptionLockTimeoutFencesAfterRetriesExhausted(t *testing.T) {
	restore := zeroSubscriptionLivenessRetryDelay(t)
	defer restore()

	connection := newSubscriptionTestConn()
	lifetime := cluster.ConnectionLifetime{Fingerprint: "fp-lock-exhausted", ConnectedAt: time.Unix(21, 0), Subscription: true}
	registry := NewFingerprintRegistry()
	tracked, errAccept := registry.Accept(context.Background(), connection, lifetime)
	if errAccept != nil {
		t.Fatal(errAccept)
	}
	defer func() { _ = tracked.Close() }()

	handler := &scriptedLivenessHandler{errs: []error{
		errors.New("canceling statement due to lock timeout"),
		errors.New("canceling statement due to lock timeout"),
		errors.New("canceling statement due to lock timeout"),
	}}
	server := New("", nil)
	server.fingerprints = registry
	server.SetClusterHandler(handler)
	cancel := server.startSubscriptionUpdates(tracked.Context(), tracked, newSafeWriter(connection))
	defer cancel()

	select {
	case <-tracked.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("exhausted lock timeouts did not fence the subscription")
	}
	if strings.Contains(connection.Output(), "pong") {
		t.Fatalf("exhausted lock timeouts wrote a pong: %q", connection.Output())
	}
	if calls := handler.calls(); calls != subscriptionLivenessAttempts {
		t.Fatalf("refresh calls = %d, want %d", calls, subscriptionLivenessAttempts)
	}
}

func zeroSubscriptionLivenessRetryDelay(t *testing.T) func() {
	t.Helper()
	saved := subscriptionLivenessRetryDelay
	subscriptionLivenessRetryDelay = 0
	return func() { subscriptionLivenessRetryDelay = saved }
}

func waitForSubscriptionOutput(t *testing.T, connection *subscriptionTestConn, fragment string, description string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(connection.Output(), fragment) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; output=%q", description, connection.Output())
}

type scriptedLivenessHandler struct {
	blockingClusterHandler
	mu    sync.Mutex
	errs  []error
	ncall int
}

func (h *scriptedLivenessHandler) RefreshCPALiveness(context.Context, cluster.ConnectionLifetime) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ncall++
	if len(h.errs) == 0 {
		return nil
	}
	errRefresh := h.errs[0]
	h.errs = h.errs[1:]
	return errRefresh
}

func (h *scriptedLivenessHandler) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ncall
}
