package agversion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAndFallback(t *testing.T) {
	if got := Version(); got == "" {
		t.Fatal("empty")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("version: 2.23.1\n")) }))
	defer srv.Close()
	old := manifestURL
	manifestURL = srv.URL
	defer func() { manifestURL = old }()
	set("")
	RefreshNow(context.Background())
	if got := UserAgent(); got != "antigravity/hub/2.23.1 darwin/arm64" {
		t.Fatalf("got %q", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("version: x\n")) }))
	defer bad.Close()
	manifestURL = bad.URL
	RefreshNow(context.Background())
	if Version() != "2.23.1" {
		t.Fatal("should keep cached on failure")
	}
	set("")
	if Version() != Fallback {
		t.Fatal("fallback")
	}
}
