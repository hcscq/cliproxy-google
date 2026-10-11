package home

import "testing"

func TestParseAntigravityCatalogHealth(t *testing.T) {
	raw := []byte(`{"models":{"gemini-3.1-pro-high":{"displayName":"Gemini 3.1 Pro (High)","tagTitle":"Leaving Soon","tagDescription":"Use Gemini 3.8 Flash!","quotaInfo":{"remainingFraction":0.76,"resetTime":"2026-10-17T05:36:57Z"}},"gemini-3-flash":{"displayName":"Gemini 3 Flash","quotaInfo":{"resetTime":"x"}}},
"deprecatedModelIds":{"gemini-3.1-pro-high":{"newModelId":"gemini-pro-agent"}}}`)
	h := parseAntigravityCatalogHealth(raw)
	m := h.models["gemini-3.1-pro-high"]
	if m.DeprecatedTo != "gemini-pro-agent" || m.TagTitle != "Leaving Soon" || m.QuotaRemaining == nil || *m.QuotaRemaining != 0.76 {
		t.Fatalf("unexpected: %#v", m)
	}
	if q := h.models["gemini-3-flash"].QuotaRemaining; q == nil || *q != 0 {
		t.Fatalf("quotaInfo without remainingFraction must mean exhausted: %v", q)
	}
	if h.deprecated["gemini-3.1-pro-high"] != "gemini-pro-agent" {
		t.Fatal("deprecated map")
	}
}

func TestClassifyProbeResponse(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{200, `{"response":{"candidates":[{"content":{"parts":[{"text":"OK"}]}}],"modelVersion":"gemini-3-flash"}}`, ProbeOK},
		{200, `{"response":{"candidates":[{"content":{"parts":[{"text":"Gemini 3.5 Flash is no longer available. Please switch to Gemini 3.7 Flash in the model picker."}]}}]}}`, ProbeRetired},
		{400, `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`, ProbeRejected},
		{429, `{"error":{"code":429,"message":"You have exhausted your capacity on this model."}}`, ProbeQuota},
		{503, `{"error":{"code":503,"message":"No capacity available for model x on the server"}}`, ProbeCapacity},
		{401, `{"error":{"code":401}}`, ProbeAuthError},
		{500, `oops`, ProbeError},
	}
	for _, c := range cases {
		if got := ClassifyProbeResponse(c.status, []byte(c.body)); got.Outcome != c.want {
			t.Errorf("%d: got %s want %s (%s)", c.status, got.Outcome, c.want, got.Message)
		}
	}
}
