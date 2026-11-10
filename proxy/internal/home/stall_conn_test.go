package home

import (
	"context"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestHomePeerFailureReleasesConnsAndDoesNotBlock(t *testing.T) {
	for _, mode := range []string{"disconnect", "halfopen", "slowread"} {
		t.Run(mode, func(t *testing.T) {
			fdsBefore := countProcessFDs(t)
			goroutinesBefore := runtime.NumGoroutine()

			listener := listenLocal(t)
			release := make(chan struct{})
			var accepted sync.WaitGroup
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				for {
					conn, errAccept := listener.Accept()
					if errAccept != nil {
						return
					}
					accepted.Add(1)
					go func(conn net.Conn) {
						defer accepted.Done()
						defer func() { _ = conn.Close() }()
						serveBadHomeConn(conn, mode, release)
					}(conn)
				}
			}()

			host, port := splitListen(t, listener)
			client := New(config.HomeConfig{
				Enabled:                 true,
				Host:                    host,
				Port:                    port,
				DisableClusterDiscovery: true,
			})
			client.testOperationTimeout = 200 * time.Millisecond

			probeDuringCall := mode == "slowread" || mode == "halfopen"
			for attempt := 0; attempt < 12; attempt++ {
				done := make(chan struct{})
				go func() {
					defer close(done)
					_ = client.LPushUsage(context.Background(), []byte(`{"usage":1}`))
				}()
				if probeDuringCall {
					select {
					case <-done:
					case <-time.After(20 * time.Millisecond):
					}
					if !client.ProbeCriticalSection(150 * time.Millisecond) {
						t.Fatal("Home client lock blocked the request path during peer I/O")
					}
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatalf("LPushUsage blocked on %s peer", mode)
				}
			}

			close(release)
			client.Close()
			_ = listener.Close()
			<-serverDone
			accepted.Wait()

			deadline := time.Now().Add(time.Second)
			for client.trackedConnCount() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := client.trackedConnCount(); got != 0 {
				t.Fatalf("tracked connections = %d after close, want 0", got)
			}

			runtime.GC()
			time.Sleep(50 * time.Millisecond)
			if fdsAfter := countProcessFDs(t); fdsAfter > fdsBefore+8 {
				t.Fatalf("process fds = %d, before %d; %s leaked sockets", fdsAfter, fdsBefore, mode)
			}
			if goroutinesAfter := runtime.NumGoroutine(); goroutinesAfter > goroutinesBefore+8 {
				t.Fatalf("goroutines = %d, before %d; %s leaked goroutines", goroutinesAfter, goroutinesBefore, mode)
			}
		})
	}
}

func TestNoteHomeUnavailableLogDoesNotReenterMutex(t *testing.T) {
	prev := homeUnavailableForceSwitch
	homeUnavailableForceSwitch = 1
	t.Cleanup(func() { homeUnavailableForceSwitch = prev })

	client := New(config.HomeConfig{Enabled: true, Host: "10.0.0.1", Port: 8327})
	client.clusterNodes = []clusterNode{
		{IP: "10.0.0.1", Port: 8327},
		{IP: "10.0.0.2", Port: 8327},
	}
	client.heartbeatOK.Store(true)

	logger := log.StandardLogger()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(oldHooks) })
	logger.AddHook(&reenterMuHook{client: client})

	done := make(chan struct{})
	go func() {
		client.NoteHomeUnavailable()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("NoteHomeUnavailable blocked in the log hook while holding Client.mu")
	}
	if !client.ProbeCriticalSection(200 * time.Millisecond) {
		t.Fatal("Client.mu still held after NoteHomeUnavailable")
	}
	if client.homeCfg.Host != "10.0.0.2" {
		t.Fatalf("host = %s, want failover to 10.0.0.2", client.homeCfg.Host)
	}
}

type reenterMuHook struct {
	client *Client
}

func (h *reenterMuHook) Levels() []log.Level { return log.AllLevels }

func (h *reenterMuHook) Fire(*log.Entry) error {
	if h.client == nil || !h.client.HeartbeatOK() {
		return nil
	}
	h.client.mu.Lock()
	h.client.mu.Unlock()
	return nil
}

func (c *Client) trackedConnCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.connections)
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	return listener
}

func splitListen(t *testing.T, listener net.Listener) (string, int) {
	t.Helper()
	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := net.LookupPort("tcp", portText)
	if errPort != nil {
		t.Fatalf("parse listener port: %v", errPort)
	}
	return host, port
}

func serveBadHomeConn(conn net.Conn, mode string, release <-chan struct{}) {
	switch mode {
	case "disconnect":
		return
	case "halfopen":
		buf := make([]byte, 1024)
		for {
			select {
			case <-release:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			_, errRead := conn.Read(buf)
			if errRead == nil {
				continue
			}
			if netErr, ok := errRead.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}
	default:
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}
}

func countProcessFDs(t *testing.T) int {
	t.Helper()
	entries, errRead := os.ReadDir("/proc/self/fd")
	if errRead != nil {
		t.Fatalf("read /proc/self/fd: %v", errRead)
	}
	return len(entries)
}
