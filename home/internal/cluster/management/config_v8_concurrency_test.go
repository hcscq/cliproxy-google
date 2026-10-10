package management

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

type configV8BlockedBody struct {
	io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (b *configV8BlockedBody) Read(buffer []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(buffer)
}

func TestConfigV8ConcurrentEditsPreserveCommittedSettings(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body, secondPath, secondBody string }{
		{"v8 path PUT", "PUT", "/config/observability/logs/debug", "true", "/config/observability/logs/logs-max-total-size-mb", "99"},
		{"v8 root PATCH with v0 write", "PATCH", "/config", `{"observability":{"logs":{"debug":true}}}`, "/v0/logs-max-total-size-mb", `{"value":99}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := openManagementLogTestDB(t)
			defer cleanup()
			repo := cluster.NewRepository(db)
			if errSeed := repo.ReplaceConfigSnapshot(context.Background(), map[string]any{"debug": false, "logs-max-total-size-mb": 40}); errSeed != nil {
				t.Fatal(errSeed)
			}
			firstHandler := NewHandler(repo, nil, "127.0.0.1", 0)
			secondHandler := NewHandler(cluster.NewRepository(db), nil, "127.0.0.2", 0)
			firstRouter, secondRouter := gin.New(), gin.New()
			firstRouter.PUT("/config/*path", firstHandler.ConfigV8)
			firstRouter.PATCH("/config", firstHandler.ConfigV8)
			secondRouter.PUT("/config/*path", secondHandler.ConfigV8)
			secondRouter.PUT("/v0/logs-max-total-size-mb", secondHandler.PutLogsMaxTotalSizeMB)
			body := &configV8BlockedBody{Reader: strings.NewReader(tc.body), entered: make(chan struct{}), release: make(chan struct{})}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response := httptest.NewRecorder()
				firstRouter.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, body))
				done <- response
			}()
			<-body.entered
			second := httptest.NewRecorder()
			secondRouter.ServeHTTP(second, httptest.NewRequest("PUT", tc.secondPath, strings.NewReader(tc.secondBody)))
			close(body.release)
			first := <-done
			if first.Code != 200 || second.Code != 200 {
				t.Fatalf("responses: %d %s; %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
			}
			snapshot, errSnapshot := repo.LoadConfigSnapshot(context.Background())
			if errSnapshot != nil {
				t.Fatal(errSnapshot)
			}
			if string(snapshot["debug"]) != "true" || string(snapshot["logs-max-total-size-mb"]) != "99" {
				t.Fatalf("lost concurrent edit: debug=%s logs-max=%s", snapshot["debug"], snapshot["logs-max-total-size-mb"])
			}
		})
	}
}
