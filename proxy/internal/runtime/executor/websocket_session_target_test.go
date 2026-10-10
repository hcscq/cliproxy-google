package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

type rejectSecondBindLifecycle struct {
	binds atomic.Int32
}

func (l *rejectSecondBindLifecycle) Bind(func() error) error {
	if l.binds.Add(1) > 1 {
		return fmt.Errorf("retry lifecycle bind rejected")
	}
	return nil
}

func (*rejectSecondBindLifecycle) End(string) {}

type trackedWebsocketLifecycle struct {
	mu    sync.Mutex
	close func() error
	once  sync.Once
	ends  atomic.Int32
}

type drainDuringBindWebsocketLifecycle struct{}

func (drainDuringBindWebsocketLifecycle) Bind(closeFn func() error) error {
	if errClose := closeFn(); errClose != nil {
		return errClose
	}
	return fmt.Errorf("execution lifecycle drained during Bind")
}

func (drainDuringBindWebsocketLifecycle) End(string) {}

func (l *trackedWebsocketLifecycle) Bind(closeFn func() error) error {
	l.mu.Lock()
	l.close = closeFn
	l.mu.Unlock()
	return nil
}

func (l *trackedWebsocketLifecycle) End(string) {
	l.once.Do(func() {
		l.ends.Add(1)
		l.mu.Lock()
		closeFn := l.close
		l.mu.Unlock()
		if closeFn != nil {
			_ = closeFn()
		}
	})
}

type closeCountingNetConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *closeCountingNetConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func newCloseCountingWebsocketConn(t *testing.T, rawURL string) (*websocket.Conn, *closeCountingNetConn) {
	t.Helper()
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		t.Fatalf("parse websocket URL: %v", errParse)
	}
	conn, errDial := net.Dial("tcp", parsed.Host)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	counting := &closeCountingNetConn{Conn: conn}
	wsConn, _, errClient := websocket.NewClient(counting, parsed, nil, 1024, 1024)
	if errClient != nil {
		_ = counting.Close()
		t.Fatalf("create websocket client: %v", errClient)
	}
	return wsConn, counting
}

func newWebsocketTargetServer(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	closed := make(chan string, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authID := r.Header.Get("X-Test-Auth")
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close upstream websocket: %v", errClose)
			}
			closed <- authID
		}()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	return server, closed
}

type registryDrainWebsocketLifecycle struct {
	scope *executionregistry.Scope
	ends  atomic.Int32
}

func (l *registryDrainWebsocketLifecycle) Bind(closeFn func() error) error {
	return l.scope.Bind(closeFn)
}

func (l *registryDrainWebsocketLifecycle) End(string) {
	l.ends.Add(1)
	l.scope.End("websocket_closed")
}

func (l *registryDrainWebsocketLifecycle) Retain() {}

type websocketHomeDispatcher struct {
	provider string
}

func (d websocketHomeDispatcher) HeartbeatOK() bool { return true }

func (d websocketHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(map[string]any{"auth": map[string]any{
		"id":       "home-websocket-auth",
		"provider": d.provider,
		"status":   "active",
		"attributes": map[string]string{
			"api_key": "home-key",
		},
	}})
}

func (websocketHomeDispatcher) AbortAmbiguousDispatch() {}

type accountedWebsocketHomeDispatcher struct {
	provider string
	baseURL  string
	calls    atomic.Int32
	releases atomic.Int32
	before   atomic.Bool
}

func (*accountedWebsocketHomeDispatcher) HeartbeatOK() bool { return true }

func (d *accountedWebsocketHomeDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	call := d.calls.Add(1)
	if call > 1 && d.releases.Load() != call-1 {
		d.before.Store(false)
	} else if call > 1 {
		d.before.Store(true)
	}
	upstreamModel := "model-a"
	if strings.Contains(strings.ToLower(model), "(custom)") {
		upstreamModel = "model-a(custom)"
	}
	return json.Marshal(map[string]any{
		"model":      upstreamModel,
		"auth_index": "accounted-websocket-auth",
		"auth": map[string]any{
			"id":       "accounted-websocket-auth",
			"provider": d.provider,
			"status":   "active",
			"attributes": map[string]string{
				"api_key":    "test-key",
				"base_url":   d.baseURL,
				"websockets": "true",
			},
		},
		"concurrency": map[string]any{
			"accounted":     true,
			"credential_id": "accounted-websocket-auth",
			"model":         upstreamModel,
		},
	})
}

func (*accountedWebsocketHomeDispatcher) AbortAmbiguousDispatch() {}

type codex426RetryDispatcher struct {
	calls                    atomic.Int32
	baseURLs                 []string
	websockets               []bool
	releases                 atomic.Int32
	releasedBeforeSecondRPop atomic.Bool
}

func (d *codex426RetryDispatcher) HeartbeatOK() bool { return true }

func (d *codex426RetryDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	call := int(d.calls.Add(1))
	if call > len(d.baseURLs) {
		return nil, fmt.Errorf("unexpected Home dispatch %d", call)
	}
	if call == 2 {
		d.releasedBeforeSecondRPop.Store(d.releases.Load() == 1)
	}
	credentialID := "codex-home-" + strconv.Itoa(call)
	attributes := map[string]string{
		"api_key":  "home-key",
		"base_url": d.baseURLs[call-1],
	}
	if call <= len(d.websockets) && d.websockets[call-1] {
		attributes["websockets"] = "true"
	}
	return json.Marshal(map[string]any{
		"model":      model,
		"auth_index": credentialID,
		"auth": map[string]any{
			"id":         credentialID,
			"provider":   "codex",
			"status":     "active",
			"attributes": attributes,
		},
		"concurrency": map[string]any{
			"accounted":     true,
			"credential_id": credentialID,
			"model":         model,
		},
	})
}

func (*codex426RetryDispatcher) AbortAmbiguousDispatch() {}
