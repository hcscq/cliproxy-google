package home

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestRecordUsagePayloadEmpty502CoolsPerModel(t *testing.T) {
	defer coreauth.SetEmpty502Policy(3, 90*time.Second)
	coreauth.SetEmpty502Policy(2, 30*time.Second)

	const model = "gemini-4-argon-medium"
	const other = "gemini-2.5-flash"
	auth := &coreauth.Auth{ID: "empty-502-usage", Index: "empty-502-usage", Provider: "antigravity", Status: coreauth.StatusActive}
	rt := newUsageResultTestRuntime(t, auth)
	t.Cleanup(func() { coreauth.ClearEmpty502Block(auth.ID, model) })

	payload := func(body string) {
		t.Helper()
		rt.RecordUsagePayload(context.Background(), `{
			"auth_index": "empty-502-usage",
			"provider": "antigravity",
			"model": "`+model+`",
			"failed": true,
			"fail": {"status_code": 502, "body": "`+body+`"}
		}`)
	}
	payload("")
	got, _ := rt.coreManager.GetByID(auth.ID)
	st := got.ModelStates[model]
	if st == nil || st.Unavailable || !st.NextRetryAfter.IsZero() {
		t.Fatalf("first empty body = %#v, want not cooling", st)
	}
	payload("upstream returned an empty response (no output)")
	got, _ = rt.coreManager.GetByID(auth.ID)
	st = got.ModelStates[model]
	delay := time.Duration(0)
	if st != nil {
		delay = time.Until(st.NextRetryAfter)
	}
	if st == nil || !st.Unavailable || delay < 20*time.Second || delay > 35*time.Second {
		t.Fatalf("second empty 502 = %#v, want ~30s backoff", st)
	}
	if otherState := got.ModelStates[other]; otherState != nil && otherState.Unavailable && otherState.NextRetryAfter.After(time.Now()) {
		t.Fatal("other model blocked")
	}
	if got.Unavailable {
		t.Fatal("whole credential marked unavailable")
	}

	rt.RecordUsagePayload(context.Background(), `{
		"auth_index": "empty-502-usage",
		"provider": "antigravity",
		"model": "`+model+`",
		"failed": false
	}`)
	got, _ = rt.coreManager.GetByID(auth.ID)
	if st := got.ModelStates[model]; st != nil && (st.Unavailable || !st.NextRetryAfter.IsZero()) {
		t.Fatalf("success left cooldown %#v", st)
	}
}
