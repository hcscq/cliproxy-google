package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func f64(v float64) *float64 { return &v }

func TestClassifySupportedModel(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	empty502 := &coreauth.ModelState{
		Status:         coreauth.StatusError,
		Unavailable:    true,
		LastError:      &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "upstream returned an empty response (no output)"},
		NextRetryAfter: past,
	}
	emptyBody502 := &coreauth.ModelState{
		Status:    coreauth.StatusError,
		LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "Bad Gateway"},
	}
	real502 := &coreauth.ModelState{
		Status:         coreauth.StatusError,
		Unavailable:    true,
		LastError:      &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "gfe 502"},
		NextRetryAfter: past,
	}
	neverCooled := &coreauth.ModelState{
		Status:    coreauth.StatusError,
		LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "upstream exploded"},
	}
	cooling := &coreauth.ModelState{Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: future, LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "gfe 502"}}
	cases := []struct {
		name string
		q    home.CatalogModelQuota
		st   *coreauth.ModelState
		want string
	}{
		{name: "remaining", q: home.CatalogModelQuota{Remaining: f64(0.4)}, want: modelStateReady},
		{name: "no quotaInfo", q: home.CatalogModelQuota{}, want: modelStateReady},
		{name: "exhausted no reset", q: home.CatalogModelQuota{Remaining: f64(0)}, want: modelStateExhausted},
		{name: "exhausted until reset", q: home.CatalogModelQuota{Remaining: f64(0), Reset: future}, want: modelStateExhausted},
		{name: "reset passed", q: home.CatalogModelQuota{Remaining: f64(0), Reset: past}, want: modelStateReady},
		{name: "cooling with quota", q: home.CatalogModelQuota{Remaining: f64(0.2)}, st: cooling, want: modelStateCooling},
		{name: "exhausted beats cooling", q: home.CatalogModelQuota{Remaining: f64(0), Reset: future}, st: cooling, want: modelStateExhausted},
		{name: "empty 502 after cooldown", q: home.CatalogModelQuota{Remaining: f64(0.8)}, st: empty502, want: modelStateError},
		{name: "empty body 502 never cooled", q: home.CatalogModelQuota{Remaining: f64(1)}, st: emptyBody502, want: modelStateError},
		{name: "empty 502 while cooling", q: home.CatalogModelQuota{Remaining: f64(1)}, st: &coreauth.ModelState{Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: future, LastError: empty502.LastError}, want: modelStateCooling},
		{name: "real 502 cooldown expired", q: home.CatalogModelQuota{Remaining: f64(1)}, st: real502, want: modelStateReady},
		{name: "error never cooled", q: home.CatalogModelQuota{Remaining: f64(1)}, st: neverCooled, want: modelStateError},
		{name: "quota flag is not error", q: home.CatalogModelQuota{Remaining: f64(1)}, st: &coreauth.ModelState{Status: coreauth.StatusError, Quota: coreauth.QuotaState{Exceeded: true}, LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}}, want: modelStateReady},
		{name: "upstream empty body 502", q: home.CatalogModelQuota{Remaining: f64(1)}, st: &coreauth.ModelState{Status: coreauth.StatusError, LastError: &coreauth.Error{Upstream: &coreauth.UpstreamResponse{Status: http.StatusBadGateway}}}, want: modelStateError},
		{name: "empty response message only", q: home.CatalogModelQuota{Remaining: f64(1)}, st: &coreauth.ModelState{StatusMessage: "upstream returned an empty response (no output)"}, want: modelStateError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySupportedModel(tc.q, tc.st, now); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCredFilterModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	longModel := "gemini-4-argon-medium"
	c.Request = httptest.NewRequest("GET", "/credentials/search?model="+longModel+"&model_status=Error", nil)
	f := credFilterFromQuery(c)
	if f.model != longModel {
		t.Fatalf("model rewritten: %q", f.model)
	}
	if f.modelStatus != modelStateError {
		t.Fatalf("model_status = %q", f.modelStatus)
	}
	// A longer id must round-trip unchanged (no truncation, no alias).
	raw := "gemini-4-argon-medium-experimental-preview"
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest("GET", "/credentials/search?model="+raw, nil)
	if got := credFilterFromQuery(c2).model; got != raw {
		t.Fatalf("model = %q, want unchanged %q", got, raw)
	}

	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	f.now = now
	f.modelQuota = map[string]home.CatalogModelQuota{
		"ready": {Remaining: f64(0.5)},
		"empty": {Remaining: f64(0.5)},
		"out":   {Remaining: f64(0), Reset: now.Add(time.Hour)},
	}
	readyAuth := &coreauth.Auth{ID: "ready"}
	emptyAuth := &coreauth.Auth{ID: "empty", ModelStates: map[string]*coreauth.ModelState{
		longModel: {Status: coreauth.StatusError, LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "Bad Gateway"}},
	}}
	outAuth := &coreauth.Auth{ID: "out"}
	other := &coreauth.Auth{ID: "other"}
	if f.modelMatch(readyAuth) {
		t.Fatal("ready credential matched model_status=error")
	}
	if !f.modelMatch(emptyAuth) {
		t.Fatal("empty 502 credential did not match model_status=error")
	}
	if f.modelMatch(outAuth) || f.modelMatch(other) {
		t.Fatal("exhausted or unsupported credential matched model_status=error")
	}

	supportOnly := f
	supportOnly.modelStatus = ""
	if !supportOnly.modelMatch(readyAuth) || !supportOnly.modelMatch(outAuth) || supportOnly.modelMatch(other) {
		t.Fatal("model filter without status must keep every credential whose catalog lists the model")
	}
	noModel := credFilter{modelStatus: modelStateError, now: now}
	if !noModel.modelMatch(other) {
		t.Fatal("model_status without model must not filter")
	}
}

func TestCredFilterModelPreservesSuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/credentials/search?model=gemini-4-argon-medium%28high%29", nil)
	if got := credFilterFromQuery(c).model; got != "gemini-4-argon-medium(high)" {
		t.Fatalf("model = %q", got)
	}
}
