package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnRecycleClosesAfterMaxRequests(t *testing.T) {
	h := withConnRecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), connRecycleConfig{maxRequests: 3})
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnContext = connRecycleContext
	srv.Start()
	defer srv.Close()
	closes := 0
	for i := 0; i < 6; i++ {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Close {
			closes++
		}
	}
	if closes != 2 {
		t.Fatalf("closes=%d want 2", closes)
	}
}
