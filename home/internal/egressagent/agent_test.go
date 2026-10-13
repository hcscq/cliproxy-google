package egressagent

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestDiscoverAddRemoveExclude(t *testing.T) {
	addrs := []netip.Addr{netip.MustParseAddr("154.40.47.154"), netip.MustParseAddr("154.40.47.179"), netip.MustParseAddr("172.16.1.2"), netip.MustParseAddr("10.42.0.1"), netip.MustParseAddr("1.2.3.4")}
	a := New(Config{
		Exclude:     map[string]bool{"1.2.3.4": true},
		ListAddrs:   func() ([]netip.Addr, error) { return addrs, nil },
		PrimaryAddr: func() (netip.Addr, error) { return netip.MustParseAddr("154.40.47.154"), nil },
	})
	added, removed, _ := a.Discover()
	if fmt.Sprint(added) != "[154.40.47.154 154.40.47.179]" || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
	inv := a.Snapshot()
	if len(inv.IPs) != 2 || !inv.IPs[0].Primary || inv.IPs[1].Primary {
		t.Fatalf("inventory %+v", inv.IPs)
	}
	addrs = []netip.Addr{netip.MustParseAddr("154.40.47.154"), netip.MustParseAddr("154.40.47.200")}
	added, removed, _ = a.Discover()
	if fmt.Sprint(added) != "[154.40.47.200]" || fmt.Sprint(removed) != "[154.40.47.179]" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

func basic(user string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"))
}

func TestProxyUser(t *testing.T) {
	if got := proxyUser(basic("154.40.47.179")); got != "154.40.47.179" {
		t.Fatal(got)
	}
	if proxyUser(basic("bob")) != "" || proxyUser("Bearer x") != "" {
		t.Fatal("non-IP user accepted")
	}
}

func TestConnectBindsSourceAndFailsClosed(t *testing.T) {
	// Echo server reporting the peer address it sees.
	echo, _ := net.Listen("tcp4", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			_, _ = io.WriteString(c, host+"\n")
			c.Close()
		}
	}()
	_, echoPort, _ := net.SplitHostPort(echo.Addr().String())
	a := New(Config{
		ListAddrs:   func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil },
		PrimaryAddr: func() (netip.Addr, error) { return netip.Addr{}, nil },
		Usable:      func(netip.Addr) bool { return true },
		Ports:       map[string]bool{echoPort: true},
	})
	_, _, _ = a.Discover()
	srv := httptest.NewServer(a)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	connect := func(user, target string) (int, string) {
		c, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		auth := ""
		if user != "" {
			auth = "Proxy-Authorization: " + basic(user) + "\r\n"
		}
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, auth)
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			return resp.StatusCode, ""
		}
		line, _ := br.ReadString('\n')
		return 200, strings.TrimSpace(line)
	}
	if code, seen := connect("127.0.0.1", "127.0.0.1:"+echoPort); code != 200 || seen != "127.0.0.1" {
		t.Fatalf("tunnel = %d %q", code, seen)
	}
	if code, _ := connect("", "127.0.0.1:"+echoPort); code != http.StatusProxyAuthRequired {
		t.Fatalf("no user = %d", code)
	}
	if code, _ := connect("9.9.9.9", "127.0.0.1:"+echoPort); code != http.StatusServiceUnavailable {
		t.Fatalf("unknown IP = %d (must fail closed)", code)
	}
	if code, _ := connect("127.0.0.1", "127.0.0.1:1"); code != http.StatusForbidden {
		t.Fatalf("other port = %d", code)
	}
	resp, err := http.Get(srv.URL + "/inventory")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("inventory %v", err)
	}
}
