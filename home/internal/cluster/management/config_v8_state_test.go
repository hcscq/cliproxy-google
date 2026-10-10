package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestConfigV8ScalarPreservesCredentialRuntimeState(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	ctx := context.Background()
	err := repo.ReplaceConfigSnapshot(ctx, map[string]any{"codex-api-key": []any{map[string]any{"api-key": "fixture-key", "base-url": "https://example.test", "models": []any{map[string]any{"name": "fixture-model"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	auths, err := repo.ListAuths(ctx)
	if err != nil || len(auths) != 1 {
		t.Fatalf("seed count=%d: %v", len(auths), err)
	}
	a := auths[0]
	until := time.Now().UTC().Add(time.Hour)
	a.Unavailable = true
	a.NextRetryAfter = until
	a.ModelStates = map[string]*coreauth.ModelState{"fixture-model": {Unavailable: true, NextRetryAfter: until, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: until}}}
	if _, err = repo.UpsertAuth(ctx, a, "update"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	engine := gin.New()
	engine.PUT("/v0/management/debug", h.PutDebug)
	engine.PUT("/v8/management/config/*path", h.ConfigV8)
	engine.GET("/v8/management/config", h.ConfigV8)
	engine.PUT("/v8/management/config", h.ConfigV8)
	for _, v := range []struct{ url, body string }{{"/v0/management/debug", `{"value":true}`}, {"/v8/management/config/observability/logs/debug", `false`}} {
		w := httptest.NewRecorder()
		q := httptest.NewRequest("PUT", v.url, strings.NewReader(v.body))
		q.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, q)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", v.url, w.Code, w.Body.String())
		}
		after, _, err := repo.GetAuth(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: unavailable=%v next_retry_after=%v model_states=%d", v.url, after.Unavailable, after.NextRetryAfter, len(after.ModelStates))
		if !after.Unavailable || !after.NextRetryAfter.Equal(until) || after.ModelStates["fixture-model"] == nil {
			t.Errorf("%s changed cooldown state while editing debug", v.url)
		}
	}

	before, recordBefore, errBefore := repo.GetAuth(ctx, a.ID)
	if errBefore != nil {
		t.Fatal(errBefore)
	}
	get := httptest.NewRecorder()
	engine.ServeHTTP(get, httptest.NewRequest("GET", "/v8/management/config", nil))
	if get.Code != 200 {
		t.Fatalf("get config = %d %s", get.Code, get.Body.String())
	}
	for _, request := range []struct{ path, body string }{
		{"/v8/management/config", get.Body.String()},
		{"/v8/management/config/api-keys/gemini", `[{"keys":[{"api-key":"new-gemini"}]}]`},
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest("PUT", request.path, strings.NewReader(request.body)))
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", request.path, response.Code, response.Body.String())
		}
		after, recordAfter, errAfter := repo.GetAuth(ctx, a.ID)
		if errAfter != nil || recordAfter.Version != recordBefore.Version || !after.NextRetryAfter.Equal(before.NextRetryAfter) || !after.Unavailable || len(after.ModelStates) != 1 {
			t.Fatalf("%s rewrote unchanged credential: %v", request.path, errAfter)
		}
	}
}

func TestConfigV8PreservesUnchangedCredentialInEditedFamily(t *testing.T) {
	for _, family := range []string{"codex", "openai-compatibility"} {
		t.Run(family, func(t *testing.T) {
			db, cleanup := openManagementLogTestDB(t)
			defer cleanup()
			repo := cluster.NewRepository(db)
			handler := NewHandler(repo, nil, "127.0.0.1", 0)
			engine := gin.New()
			engine.PUT("/config/*path", handler.ConfigV8)
			engine.GET("/config/*path", handler.ConfigV8)
			path := "/config/api-keys/" + family
			request := func(method, body string) *httptest.ResponseRecorder {
				t.Helper()
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
				if response.Code != 200 {
					t.Fatalf("%s: %d %s", method, response.Code, response.Body.String())
				}
				return response
			}
			request("PUT", `[{"name":"group","base-url":"https://example.test","models":[{"name":"model"}],"keys":[{"api-key":"first"},{"api-key":"second"}]}]`)
			auths, errAuths := repo.ListAuths(context.Background())
			if errAuths != nil || len(auths) != 2 {
				t.Fatalf("credentials=%d err=%v", len(auths), errAuths)
			}
			var untouched *coreauth.Auth
			for _, auth := range auths {
				if auth.Attributes["api_key"] == "second" {
					untouched = auth
				}
			}
			if untouched == nil {
				t.Fatal("second credential is missing")
			}
			until := time.Now().UTC().Add(time.Hour)
			untouched.Unavailable = true
			untouched.NextRetryAfter = until
			untouched.LastRefreshError = &coreauth.Error{Code: "fixture", Message: "retained refresh error"}
			untouched.ModelStates = map[string]*coreauth.ModelState{"model": {Unavailable: true, NextRetryAfter: until, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: until}}}
			before, errSave := repo.UpsertAuth(context.Background(), untouched, "update")
			if errSave != nil {
				t.Fatal(errSave)
			}
			response := request("GET", "")
			var groups []map[string]any
			if errDecode := json.Unmarshal(response.Body.Bytes(), &groups); errDecode != nil {
				t.Fatal(errDecode)
			}
			for _, group := range groups {
				for _, raw := range group["keys"].([]any) {
					key := raw.(map[string]any)
					if key["api-key"] == "first" {
						key["weight"] = 2
					}
				}
			}
			raw, errEncode := json.Marshal(groups)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			request("PUT", string(raw))
			_, after, errGet := repo.GetAuth(context.Background(), untouched.ID)
			if errGet != nil {
				t.Fatal(errGet)
			}
			if after.Version != before.Version || !bytes.Equal(after.AuthJSON, before.AuthJSON) {
				t.Fatal("changing the first credential rewrote the second credential's runtime state or version")
			}
			updated, errList := repo.ListAuths(context.Background())
			if errList != nil {
				t.Fatal(errList)
			}
			for _, auth := range updated {
				if auth.Attributes["api_key"] == "first" && auth.Attributes["weight"] != "2" {
					t.Fatal("the changed credential was not updated")
				}
			}
		})
	}
}
